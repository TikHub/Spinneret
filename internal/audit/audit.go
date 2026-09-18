// Package audit records security-relevant operations (admin mutations, secret
// reads, logins) into the partitioned audit_logs table. Writes are buffered
// and flushed in batches so that request handlers never block on the database.
//
// Entries are sanitized when they are recorded: text fields and the strings
// of Details are coerced to valid UTF-8 and NUL characters are replaced with
// U+FFFD, because PostgreSQL rejects both in text and jsonb values. A batch
// that the database still rejects is split until the offending entries are
// isolated, so one bad entry only loses itself; connection-level failures are
// retried with backoff before a batch is given up.
package audit

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/pkg/idgen"
)

// Result values.
const (
	ResultOK     = "ok"
	ResultDenied = "denied"
	ResultError  = "error"
)

// Writer defaults.
const (
	defaultBufferSize       = 50_000
	defaultBatchSize        = 500
	defaultFlushInterval    = time.Second
	defaultStatementTimeout = 10 * time.Second
	defaultMaxAttempts      = 3
	defaultRetryBackoff     = 100 * time.Millisecond
	maxRetryBackoff         = 2 * time.Second
	// drainTimeout bounds the final flush after Run's context ends.
	drainTimeout = 5 * time.Second
	// bufferFullLogEvery throttles the "buffer full" log line.
	bufferFullLogEvery = 1000
)

// Entry is one audit record.
type Entry struct {
	CreatedAt    time.Time
	TenantID     string
	NamespaceID  string
	ActorKind    string // user | token | system
	ActorID      string
	ActorName    string
	Action       string // e.g. "identity.import", "secret.read", "auth.login"
	ResourceKind string
	ResourceID   string
	ResourceName string
	Result       string
	IP           string
	UserAgent    string
	Details      map[string]any
}

// Recorder records audit entries. Implementations must not block for long and
// must be safe for concurrent use.
type Recorder interface {
	Record(ctx context.Context, e Entry)
}

// Nop discards all entries.
type Nop struct{}

// Record implements Recorder.
func (Nop) Record(context.Context, Entry) {}

// FromPrincipal builds an entry for an operation performed by p.
func FromPrincipal(p *authz.Principal, tenantID, namespaceID, action, resourceKind, resourceID, resourceName, result string, details map[string]any) Entry {
	e := Entry{
		TenantID:     tenantID,
		NamespaceID:  namespaceID,
		Action:       action,
		ResourceKind: resourceKind,
		ResourceID:   resourceID,
		ResourceName: resourceName,
		Result:       result,
		Details:      details,
		ActorKind:    string(authz.KindSystem),
	}
	if p != nil {
		e.ActorKind = string(p.Kind)
		e.ActorID = p.ID
		e.ActorName = p.Name
		e.IP = p.ClientIP
		e.UserAgent = p.UserAgent
		if e.TenantID == "" {
			e.TenantID = p.TenantID
		}
	}
	return e
}

// record is a sanitized entry ready to be written. It shares no memory with
// the caller's Entry (Details is encoded when the entry is recorded).
type record struct {
	id           string
	createdAt    time.Time
	tenantID     string
	namespaceID  string
	actorKind    string
	actorID      string
	actorName    string
	action       string
	resourceKind string
	resourceID   string
	resourceName string
	result       string
	ip           string
	userAgent    string
	details      []byte
}

// newRecord sanitizes e and applies the defaults of Writer.Record.
func newRecord(e Entry) record {
	createdAt := e.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	result := e.Result
	if result == "" {
		result = ResultOK
	}
	return record{
		id:           idgen.New(idgen.Audit),
		createdAt:    createdAt,
		tenantID:     cleanText(e.TenantID),
		namespaceID:  cleanText(e.NamespaceID),
		actorKind:    cleanText(e.ActorKind),
		actorID:      cleanText(e.ActorID),
		actorName:    cleanText(e.ActorName),
		action:       cleanText(e.Action),
		resourceKind: cleanText(e.ResourceKind),
		resourceID:   cleanText(e.ResourceID),
		resourceName: cleanText(e.ResourceName),
		result:       cleanText(result),
		ip:           cleanText(e.IP),
		userAgent:    cleanText(e.UserAgent),
		details:      encodeDetails(e.Details),
	}
}

// values returns the COPY row of r in auditColumns order.
func (r record) values() []any {
	return []any{
		r.id, r.createdAt, r.tenantID, r.namespaceID, r.actorKind, r.actorID, r.actorName,
		r.action, r.resourceKind, r.resourceID, r.resourceName, r.result, r.ip, r.userAgent, r.details,
	}
}

// Writer is a buffered Recorder backed by PostgreSQL.
type Writer struct {
	pool      *pgxpool.Pool
	logger    *slog.Logger
	ch        chan record
	batchSize int
	interval  time.Duration

	statementTimeout time.Duration
	maxAttempts      int
	retryBackoff     time.Duration
	// copyFrom writes rows with one COPY statement (replaced by tests).
	copyFrom func(ctx context.Context, rows [][]any) error

	dropped atomic.Int64
}

// NewWriter creates a writer with a bounded buffer.
func NewWriter(pool *pgxpool.Pool, logger *slog.Logger) *Writer {
	if logger == nil {
		logger = slog.Default()
	}
	w := &Writer{
		pool:             pool,
		logger:           logger,
		ch:               make(chan record, defaultBufferSize),
		batchSize:        defaultBatchSize,
		interval:         defaultFlushInterval,
		statementTimeout: defaultStatementTimeout,
		maxAttempts:      defaultMaxAttempts,
		retryBackoff:     defaultRetryBackoff,
	}
	w.copyFrom = w.copyRows
	return w
}

// Record sanitizes and enqueues an entry. When the buffer is full the entry is
// dropped and counted, because audit logging must never take the API down.
func (w *Writer) Record(_ context.Context, e Entry) {
	select {
	case w.ch <- newRecord(e):
	default:
		if n := w.dropped.Add(1); n%bufferFullLogEvery == 1 {
			w.logger.Error("audit buffer full, dropping entries", slog.Int64("dropped_total", n))
		}
	}
}

// Dropped returns the number of entries that were not persisted: dropped
// because the buffer was full, rejected by the database, or given up after
// write failures.
func (w *Writer) Dropped() int64 { return w.dropped.Load() }

// Run flushes buffered entries until ctx is canceled, then drains the buffer
// within a bounded time.
func (w *Writer) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	batch := make([]record, 0, w.batchSize)
	for {
		select {
		case <-ctx.Done():
			w.drain(batch)
			return nil
		case r := <-w.ch:
			batch = append(batch, r)
			if len(batch) >= w.batchSize {
				batch = w.flush(ctx, batch)
			}
		case <-ticker.C:
			batch = w.flush(ctx, batch)
		}
	}
}

// drain writes the pending batch and the buffered entries after Run's context
// ended. Entries still unwritten when drainTimeout expires are counted as
// dropped.
func (w *Writer) drain(batch []record) {
	dctx, cancel := context.WithTimeout(context.Background(), drainTimeout)
	defer cancel()
	for {
		batch = w.fill(batch)
		if len(batch) == 0 {
			return
		}
		batch = w.flush(dctx, batch)
		if len(batch) > 0 || dctx.Err() != nil {
			w.lose(len(batch)+len(w.ch), "audit drain timed out, dropping entries", dctx.Err())
			return
		}
	}
}

// fill appends buffered entries to batch without blocking, up to the batch size.
func (w *Writer) fill(batch []record) []record {
	for len(batch) < w.batchSize {
		select {
		case r := <-w.ch:
			batch = append(batch, r)
		default:
			return batch
		}
	}
	return batch
}

// flush writes batch. It returns an empty slice reusing batch's storage, or
// the entries kept for a later flush because ctx ended while a transient
// failure was being retried.
func (w *Writer) flush(ctx context.Context, batch []record) []record {
	if len(batch) == 0 {
		return batch
	}
	if kept := w.writeBatch(ctx, batch); len(kept) > 0 {
		return kept
	}
	return batch[:0]
}
