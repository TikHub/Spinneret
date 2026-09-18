package stats

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Evil0ctal/Spinneret/internal/pkg/idgen"
	"github.com/Evil0ctal/Spinneret/internal/store/postgres"
)

// riskColumns lists the risk_events columns written by the aggregator, in the
// order produced by riskRow.values.
var riskColumns = []string{
	"id", "created_at", "tenant_id", "namespace_id", "site_id", "endpoint_group_id", "identity_id", "proxy_id",
	"lease_id", "report_id", "node", "token_id", "uri", "method", "http_status", "business_code", "error_kind",
	"markers", "outcome", "blame", "rule", "latency_ms", "response_bytes", "started_at", "finished_at",
}

// riskTempTable is the transaction-scoped staging table used when a COPY hits
// rows that already exist (a retried batch whose commit acknowledgement was lost).
const riskTempTable = "stats_risk_events_staging"

// riskRow is one buffered risk_events row.
type riskRow struct {
	id                                                    string
	createdAt                                             time.Time
	tenantID, namespaceID, siteID, endpointGroupID        string
	identityID, proxyID, leaseID, reportID, node, tokenID string
	uri, method                                           string
	httpStatus                                            int32
	businessCode, errorKind                               string
	markers                                               []string
	outcome, blame, rule                                  string
	latencyMs                                             int32
	responseBytes                                         int64
	startedAt, finishedAt                                 *time.Time
}

// riskRowOverhead approximates the in-memory size of a riskRow without its
// string contents (struct, time pointers, slice header).
const riskRowOverhead = 512

// newRiskRow builds a sanitized row from a report attributed to eventTime.
// markers must already be sanitized (cleanMarkers) and must not be modified
// afterwards.
func newRiskRow(r *ReportRecord, eventTime time.Time, outcome string, markers []string) riskRow {
	return riskRow{
		createdAt:       eventTime.UTC(),
		tenantID:        cleanText(r.TenantID, maxIDBytes),
		namespaceID:     cleanText(r.NamespaceID, maxIDBytes),
		siteID:          cleanText(r.SiteID, maxIDBytes),
		endpointGroupID: cleanText(r.EndpointGroupID, maxIDBytes),
		identityID:      cleanText(r.IdentityID, maxIDBytes),
		proxyID:         cleanText(r.ProxyID, maxIDBytes),
		leaseID:         cleanText(r.LeaseID, maxIDBytes),
		reportID:        cleanText(r.ReportID, maxIDBytes),
		node:            cleanText(r.Node, maxNodeBytes),
		tokenID:         cleanText(r.TokenID, maxIDBytes),
		uri:             cleanText(r.URI, maxURIBytes),
		method:          cleanText(r.Method, maxEnumBytes),
		httpStatus:      clampInt32(int64(r.HTTPStatus)),
		businessCode:    cleanText(r.BusinessCode, maxShortBytes),
		errorKind:       cleanText(r.ErrorKind, maxShortBytes),
		markers:         markers,
		outcome:         outcome,
		blame:           cleanText(r.Blame, maxEnumBytes),
		rule:            cleanText(r.Rule, maxShortBytes),
		latencyMs:       clampInt32(r.LatencyMs),
		responseBytes:   nonNegative(r.ResponseBytes),
		startedAt:       optionalTime(r.StartedAt),
		finishedAt:      optionalTime(r.FinishedAt),
	}
}

// size approximates the memory held by the row, used for the byte budget.
func (r *riskRow) size() int {
	n := riskRowOverhead + len(r.id) + len(r.tenantID) + len(r.namespaceID) + len(r.siteID) +
		len(r.endpointGroupID) + len(r.identityID) + len(r.proxyID) + len(r.leaseID) + len(r.reportID) +
		len(r.node) + len(r.tokenID) + len(r.uri) + len(r.method) + len(r.businessCode) + len(r.errorKind) +
		len(r.outcome) + len(r.blame) + len(r.rule)
	for _, m := range r.markers {
		n += len(m) + 16
	}
	return n
}

// values returns the column values in riskColumns order.
func (r riskRow) values() []any {
	return []any{
		r.id, r.createdAt, r.tenantID, r.namespaceID, r.siteID, r.endpointGroupID, r.identityID, r.proxyID,
		r.leaseID, r.reportID, r.node, r.tokenID, r.uri, r.method, r.httpStatus, r.businessCode, r.errorKind,
		r.markers, r.outcome, r.blame, r.rule, r.latencyMs, r.responseBytes, r.startedAt, r.finishedAt,
	}
}

// riskBuffer is the bounded buffer of risk_events rows. Both the rows
// buffered since the last flush and the rows pending after failed flushes are
// capped by count and by approximate size. add is safe for concurrent use;
// drain and flush are called only by the flusher.
type riskBuffer struct {
	capacity int
	maxBytes int

	mu    sync.Mutex
	rows  []riskRow
	bytes int

	// droppedFull counts rows rejected because the buffer was full.
	droppedFull atomic.Int64
	// droppedRows counts rows discarded while flushing.
	droppedRows atomic.Int64

	// pending holds rows not yet written, oldest first (flusher only).
	pending []riskRow
	// exec writes one chunk of rows.
	exec func(ctx context.Context, rows []riskRow) error
}

func newRiskBuffer(capacity, maxBytes int, exec func(context.Context, []riskRow) error) *riskBuffer {
	return &riskBuffer{capacity: capacity, maxBytes: maxBytes, exec: exec}
}

// add appends a row unless the buffer is full (by count or size).
func (b *riskBuffer) add(r riskRow) {
	size := r.size()
	b.mu.Lock()
	if len(b.rows) >= b.capacity || b.bytes+size > b.maxBytes {
		b.mu.Unlock()
		b.droppedFull.Add(1)
		return
	}
	b.rows = append(b.rows, r)
	b.bytes += size
	b.mu.Unlock()
}

// drain moves buffered rows to pending, assigns ids and enforces the caps by
// dropping the oldest pending rows.
func (b *riskBuffer) drain() {
	b.mu.Lock()
	rows := b.rows
	b.rows = nil
	b.bytes = 0
	b.mu.Unlock()

	for i := range rows {
		rows[i].id = idgen.New(idgen.RiskEvent)
	}
	if len(b.pending) == 0 {
		b.pending = rows
	} else {
		b.pending = append(b.pending, rows...)
	}
	b.trim()
}

// trim drops the oldest pending rows until pending fits the count and size caps.
func (b *riskBuffer) trim() {
	excess := max(len(b.pending)-b.capacity, 0)
	total := 0
	for i := excess; i < len(b.pending); i++ {
		total += b.pending[i].size()
	}
	for excess < len(b.pending) && total > b.maxBytes {
		total -= b.pending[excess].size()
		excess++
	}
	if excess == 0 {
		return
	}
	kept := make([]riskRow, len(b.pending)-excess)
	copy(kept, b.pending[excess:])
	b.pending = kept
	b.droppedRows.Add(int64(excess))
}

// flush drains the buffer and copies pending rows in chunks.
func (b *riskBuffer) flush(ctx context.Context, fw *flushWriter) error {
	b.drain()
	if len(b.pending) == 0 {
		return nil
	}
	rows := b.pending
	w := newRowWriter(fw, postgres.TableRiskEvents, postgres.GranularityDay,
		func(r riskRow) int64 { return r.createdAt.Unix() }, b.exec)
	var kept []riskRow
	for start := 0; start < len(rows); start += fw.chunkSize {
		end := min(start+fw.chunkSize, len(rows))
		if w.stopped(ctx) {
			kept = append(kept, rows[start:]...)
			break
		}
		failed, dropped := w.write(ctx, rows[start:end])
		kept = append(kept, failed...)
		b.droppedRows.Add(int64(dropped))
	}
	b.pending = kept
	if len(kept) > 0 {
		return fmt.Errorf("flush %s: %d rows kept for the next flush", postgres.TableRiskEvents, len(kept))
	}
	return nil
}

// copyRiskEvents writes rows with COPY. When a row already exists (a batch
// whose commit acknowledgement was lost and that is now retried), the rows are
// staged in a temporary table and inserted with ON CONFLICT DO NOTHING.
func copyRiskEvents(ctx context.Context, pool *pgxpool.Pool, rows []riskRow) error {
	if pool == nil {
		return errNoPool
	}
	_, err := postgres.CopyRows(ctx, pool, postgres.TableRiskEvents, riskColumns, rows, riskRow.values)
	if err == nil || !isUniqueViolation(err) {
		return err
	}
	return insertRiskEventsIgnoringDuplicates(ctx, pool, rows)
}

// insertRiskEventsIgnoringDuplicates stages rows in a temporary table and
// inserts those that do not exist yet.
func insertRiskEventsIgnoringDuplicates(ctx context.Context, pool *pgxpool.Pool, rows []riskRow) error {
	cols := pgx.Identifier{riskColumns[0]}.Sanitize()
	for _, c := range riskColumns[1:] {
		cols += ", " + pgx.Identifier{c}.Sanitize()
	}
	tmp := pgx.Identifier{riskTempTable}.Sanitize()
	err := pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		create := "CREATE TEMPORARY TABLE " + tmp + " (LIKE " + pgx.Identifier{postgres.TableRiskEvents}.Sanitize() +
			" INCLUDING DEFAULTS) ON COMMIT DROP"
		if _, err := tx.Exec(ctx, create); err != nil {
			return fmt.Errorf("create staging table: %w", err)
		}
		if _, err := postgres.CopyRows(ctx, tx, riskTempTable, riskColumns, rows, riskRow.values); err != nil {
			return err
		}
		insert := "INSERT INTO " + pgx.Identifier{postgres.TableRiskEvents}.Sanitize() + " (" + cols + ") SELECT " +
			cols + " FROM " + tmp + " ON CONFLICT DO NOTHING"
		if _, err := tx.Exec(ctx, insert); err != nil {
			return fmt.Errorf("insert staged risk events: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("insert risk events ignoring duplicates: %w", err)
	}
	return nil
}

// isUniqueViolation reports whether err is a unique constraint violation.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == postgres.SQLStateUniqueViolation
}
