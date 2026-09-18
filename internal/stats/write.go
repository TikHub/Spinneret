package stats

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/TikHub/Spinneret/internal/observability"
	"github.com/TikHub/Spinneret/internal/store/postgres"
)

// DBWriteBatches result label values.
const (
	batchOK      = "ok"
	batchRetry   = "retry"
	batchError   = "error"
	batchDropped = "dropped"
)

// sqlStateProgramLimitExceeded is raised for example when an index key exceeds
// the btree maximum size.
const sqlStateProgramLimitExceeded = "54000"

// ensureState tracks the partition repair attempt of one flush.
type ensureState int

const (
	ensureNotTried ensureState = iota
	ensureOK
	ensureFailed
)

// flushWriter carries the state shared by all writes of one flush.
type flushWriter struct {
	metrics          *observability.Metrics
	logger           *slog.Logger
	chunkSize        int
	maxRetries       int
	retryBackoff     time.Duration
	statementTimeout time.Duration
	ensure           func(ctx context.Context) error

	ensureState ensureState
	// failed is set once the database is unreachable (a connection-level
	// error exhausted its retries); the remaining writes of the flush are
	// skipped and their rows kept.
	failed bool
}

// stopped reports whether the remaining writes of this flush must be skipped.
func (fw *flushWriter) stopped(ctx context.Context) bool {
	return fw.failed || ctx.Err() != nil
}

// count increments DBWriteBatches for table and result.
func (fw *flushWriter) count(table, result string) {
	if fw.metrics == nil {
		return
	}
	fw.metrics.DBWriteBatches.WithLabelValues(table, result).Inc()
}

// ensurePartitions runs the partition repair at most once per flush and
// reports whether partitions are known to be ensured.
func (fw *flushWriter) ensurePartitions(ctx context.Context, table string) bool {
	if fw.ensureState == ensureNotTried {
		if err := fw.ensure(ctx); err != nil {
			fw.ensureState = ensureFailed
			fw.logger.Error("stats: ensure partitions failed", slog.String("table", table), slog.Any("error", err))
		} else {
			fw.ensureState = ensureOK
			fw.logger.Info("stats: created missing partitions", slog.String("table", table))
		}
	}
	return fw.ensureState == ensureOK
}

// rowWriter writes rows of one table with retries, partition repair and
// isolation of rows that can never be written.
type rowWriter[R any] struct {
	fw          *flushWriter
	table       string
	granularity postgres.Granularity
	bucketOf    func(R) int64
	exec        func(context.Context, []R) error
	// halted is set when a statement of this table failed with a server error
	// that is not specific to the rows (after retries when retryable); the
	// table's remaining writes of the flush are skipped and their rows kept,
	// while other tables are still written.
	halted *bool
}

// newRowWriter returns a writer for one table flush.
func newRowWriter[R any](fw *flushWriter, table string, g postgres.Granularity, bucketOf func(R) int64,
	exec func(context.Context, []R) error) rowWriter[R] {
	return rowWriter[R]{fw: fw, table: table, granularity: g, bucketOf: bucketOf, exec: exec, halted: new(bool)}
}

// stopped reports whether the remaining writes of this table must be skipped.
func (w rowWriter[R]) stopped(ctx context.Context) bool {
	return *w.halted || w.fw.stopped(ctx)
}

// write persists rows and returns the rows to keep for a later flush plus the
// number of rows dropped permanently.
func (w rowWriter[R]) write(ctx context.Context, rows []R) (kept []R, dropped int) {
	if len(rows) == 0 {
		return nil, 0
	}
	if w.stopped(ctx) {
		return rows, 0
	}
	err := w.execRetry(ctx, rows)
	if err == nil {
		return nil, 0
	}
	return w.handle(ctx, rows, err)
}

// execRetry runs exec, retrying transient errors with exponential backoff.
func (w rowWriter[R]) execRetry(ctx context.Context, rows []R) error {
	for attempt := 0; ; attempt++ {
		actx, cancel := context.WithTimeout(ctx, w.fw.statementTimeout)
		err := w.exec(actx, rows)
		cancel()
		if err == nil {
			w.fw.count(w.table, batchOK)
			return nil
		}
		if ctx.Err() != nil || !isRetryable(err) || attempt >= w.fw.maxRetries {
			return err
		}
		w.fw.count(w.table, batchRetry)
		w.fw.logger.Warn("stats: write failed, retrying",
			slog.String("table", w.table), slog.Int("rows", len(rows)), slog.Int("attempt", attempt+1), slog.Any("error", err))
		timer := time.NewTimer(w.fw.retryBackoff << attempt)
		select {
		case <-ctx.Done():
			timer.Stop()
			return err
		case <-timer.C:
		}
	}
}

// handle resolves a failed write according to the error class.
func (w rowWriter[R]) handle(ctx context.Context, rows []R, err error) (kept []R, dropped int) {
	switch {
	case ctx.Err() != nil:
		return rows, 0
	case isMissingPartition(err):
		return w.handleMissingPartition(ctx, rows)
	case isDataError(err):
		if len(rows) == 1 {
			w.drop(1, "row rejected by the database", err)
			return nil, 1
		}
		mid := len(rows) / 2
		k1, d1 := w.write(ctx, rows[:mid])
		k2, d2 := w.write(ctx, rows[mid:])
		// Concat allocates: k1 may alias rows, and appending to it in place
		// would overwrite rows the caller still reads.
		return slices.Concat(k1, k2), d1 + d2
	default:
		// When the database as a whole is unavailable the rest of the flush
		// is skipped; a statement-level error only stops this table.
		if isDatabaseUnavailable(err) {
			w.fw.failed = true
		} else {
			*w.halted = true
		}
		w.fw.count(w.table, batchError)
		w.fw.logger.Error("stats: write failed, rows kept for the next flush",
			slog.String("table", w.table), slog.Int("rows", len(rows)), slog.Any("error", err))
		return rows, 0
	}
}

// handleMissingPartition repairs partitions once per flush and retries; rows
// whose partition period still does not exist are dropped.
func (w rowWriter[R]) handleMissingPartition(ctx context.Context, rows []R) (kept []R, dropped int) {
	if w.fw.ensureState == ensureNotTried {
		if !w.fw.ensurePartitions(ctx, w.table) {
			w.fw.failed = true
			w.fw.count(w.table, batchError)
			return rows, 0
		}
		return w.write(ctx, rows)
	}
	if w.fw.ensureState == ensureFailed {
		w.fw.count(w.table, batchError)
		return rows, 0
	}
	groups := w.groupByPartition(rows)
	if len(groups) == 1 {
		w.drop(len(rows), "no partition for bucket", nil,
			slog.Time("partition_start", unixTime(w.partitionStart(w.bucketOf(rows[0])))))
		return nil, len(rows)
	}
	for _, g := range groups {
		k, d := w.write(ctx, g)
		kept = append(kept, k...)
		dropped += d
	}
	return kept, dropped
}

// drop records permanently discarded rows.
func (w rowWriter[R]) drop(n int, reason string, err error, attrs ...any) {
	w.fw.count(w.table, batchDropped)
	args := append([]any{slog.String("table", w.table), slog.Int("rows", n), slog.String("reason", reason)}, attrs...)
	if err != nil {
		args = append(args, slog.Any("error", err))
	}
	w.fw.logger.Error("stats: rows dropped", args...)
}

// partitionStart returns the start of the partition period containing unix.
func (w rowWriter[R]) partitionStart(unix int64) int64 {
	if w.granularity == postgres.GranularityMonth {
		return monthStart(unix)
	}
	return dayStart(unix)
}

// groupByPartition splits rows by partition period, preserving row order
// within each group and the order of first appearance between groups.
func (w rowWriter[R]) groupByPartition(rows []R) [][]R {
	index := make(map[int64]int)
	var groups [][]R
	for _, r := range rows {
		p := w.partitionStart(w.bucketOf(r))
		i, ok := index[p]
		if !ok {
			i = len(groups)
			index[p] = i
			groups = append(groups, nil)
		}
		groups[i] = append(groups[i], r)
	}
	return groups
}

// isMissingPartition reports whether err is PostgreSQL's "no partition of
// relation … found for row". The server raises it as a check violation
// without a constraint name (unlike real CHECK constraints), which keeps the
// classification independent of the server's message locale.
func isMissingPartition(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != postgres.SQLStateCheckViolation {
		return false
	}
	return pgErr.ConstraintName == "" || strings.Contains(pgErr.Message, "no partition of relation")
}

// isDataError reports whether err is caused by the written values themselves
// (SQLSTATE classes 22 and 23 except missing partitions, program limit
// exceeded), so retrying the same rows cannot succeed.
func isDataError(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || isMissingPartition(err) {
		return false
	}
	return strings.HasPrefix(pgErr.Code, "22") || strings.HasPrefix(pgErr.Code, "23") ||
		pgErr.Code == sqlStateProgramLimitExceeded
}

// sqlStateQueryCanceled is raised when a statement is canceled (e.g. by
// statement_timeout); it concerns only that statement.
const sqlStateQueryCanceled = "57014"

// isDatabaseUnavailable reports whether err means that no statement can
// currently succeed: client-side failures (connection loss, timeouts, no
// pool) and server errors of the SQLSTATE classes 08 (connection exception),
// 53 (insufficient resources), 57 (operator intervention, except a canceled
// statement) and 58 (system error).
func isDatabaseUnavailable(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return true
	}
	if len(pgErr.Code) < 2 || pgErr.Code == sqlStateQueryCanceled {
		return false
	}
	switch pgErr.Code[:2] {
	case "08", "53", "57", "58":
		return true
	default:
		return false
	}
}

// isRetryable reports whether retrying the same statement right away may
// succeed: connection-level failures and timeouts, and server errors of the
// SQLSTATE classes 08 (connection exception), 40 (transaction rollback, e.g.
// deadlocks), 53 (insufficient resources), 55 (object not in prerequisite
// state, e.g. lock timeouts), 57 (operator intervention, e.g. shutdown or
// statement cancel) and 58 (system error). Missing partitions, data errors
// and permanent server errors (e.g. undefined table, insufficient privilege)
// are not retried.
func isRetryable(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return true
	}
	if len(pgErr.Code) < 2 {
		return false
	}
	switch pgErr.Code[:2] {
	case "08", "40", "53", "55", "57", "58":
		return true
	default:
		return false
	}
}
