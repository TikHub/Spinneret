package server

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/TikHub/Spinneret/internal/observability"
	"github.com/TikHub/Spinneret/internal/scheduler"
)

// Proxy binding writer defaults.
const (
	bindingQueueSize     = 50_000
	bindingFlushInterval = time.Second
	bindingMaxBatch      = 1000
	bindingWriteAttempts = 3
	bindingWriteTimeout  = 10 * time.Second
	bindingDrainTimeout  = 10 * time.Second
	bindingWriterName    = "proxy_bindings"
)

// upsertProxyBindingsSQL persists identity → proxy bindings created by acquire
// in bind_identity mode. Rows whose identity or proxy no longer exists are
// skipped (instead of failing the batch on the foreign keys), and an older
// binding never overwrites a newer one written by another instance.
const upsertProxyBindingsSQL = `
INSERT INTO proxy_bindings (identity_id, proxy_id, bound_at, rebind_day, rebinds_today)
SELECT u.identity_id, u.proxy_id, u.bound_at, u.rebind_day::date, u.rebinds_today
FROM unnest($1::text[], $2::text[], $3::timestamptz[], $4::text[], $5::int4[])
    AS u(identity_id, proxy_id, bound_at, rebind_day, rebinds_today)
WHERE EXISTS (SELECT 1 FROM identities i WHERE i.id = u.identity_id)
  AND EXISTS (SELECT 1 FROM proxies p WHERE p.id = u.proxy_id)
ORDER BY u.identity_id
ON CONFLICT (identity_id) DO UPDATE
SET proxy_id = EXCLUDED.proxy_id,
    bound_at = EXCLUDED.bound_at,
    rebind_day = EXCLUDED.rebind_day,
    rebinds_today = EXCLUDED.rebinds_today
WHERE proxy_bindings.bound_at <= EXCLUDED.bound_at`

// bindingExecer executes the upsert (satisfied by *pgxpool.Pool).
type bindingExecer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// proxyBindingWriter persists scheduler proxy bindings asynchronously in
// batches. Record never blocks the acquire path: bindings that do not fit into
// the queue are dropped and counted (Redis stays authoritative for routing and
// the next rebind or hot-state snapshot writes the binding again).
type proxyBindingWriter struct {
	db      bindingExecer
	metrics *observability.Metrics
	logger  *slog.Logger

	queue      chan scheduler.ProxyBinding
	flushEvery time.Duration
	maxBatch   int
	retryDelay time.Duration

	dropped atomic.Int64
	written atomic.Int64
}

func newProxyBindingWriter(db bindingExecer, metrics *observability.Metrics, logger *slog.Logger) *proxyBindingWriter {
	return &proxyBindingWriter{
		db:         db,
		metrics:    metrics,
		logger:     logger.With(slog.String("component", "proxy_bindings")),
		queue:      make(chan scheduler.ProxyBinding, bindingQueueSize),
		flushEvery: bindingFlushInterval,
		maxBatch:   bindingMaxBatch,
		retryDelay: 200 * time.Millisecond,
	}
}

// Record queues a binding; it is the scheduler.Config.OnProxyBound callback.
func (w *proxyBindingWriter) Record(b scheduler.ProxyBinding) {
	if b.IdentityID == "" || b.ProxyID == "" {
		return
	}
	select {
	case w.queue <- b:
	default:
		if n := w.dropped.Add(1); n%1000 == 1 {
			w.logger.Warn("proxy binding queue full, dropping bindings", slog.Int64("dropped_total", n))
		}
		w.count("dropped")
	}
}

// Run flushes queued bindings until ctx is canceled, then drains the queue
// with a bounded timeout.
func (w *proxyBindingWriter) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.flushEvery)
	defer ticker.Stop()
	pending := make(map[string]scheduler.ProxyBinding)
	for {
		select {
		case <-ctx.Done():
			drainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), bindingDrainTimeout)
			defer cancel()
			for {
				select {
				case b := <-w.queue:
					coalesceBinding(pending, b)
					if len(pending) >= w.maxBatch {
						w.flush(drainCtx, pending)
					}
					continue
				default:
				}
				break
			}
			w.flush(drainCtx, pending)
			return nil
		case b := <-w.queue:
			coalesceBinding(pending, b)
			if len(pending) >= w.maxBatch {
				w.flush(ctx, pending)
			}
		case <-ticker.C:
			w.flush(ctx, pending)
		}
	}
}

// coalesceBinding keeps only the most recent binding per identity.
func coalesceBinding(pending map[string]scheduler.ProxyBinding, b scheduler.ProxyBinding) {
	if cur, ok := pending[b.IdentityID]; ok && cur.At.After(b.At) {
		return
	}
	pending[b.IdentityID] = b
}

// flush writes and clears pending. A batch that still fails after the retries
// is dropped and counted.
func (w *proxyBindingWriter) flush(ctx context.Context, pending map[string]scheduler.ProxyBinding) {
	if len(pending) == 0 {
		return
	}
	args := bindingArgs(pending)
	clear(pending)
	var err error
	for attempt := 1; attempt <= bindingWriteAttempts; attempt++ {
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), bindingWriteTimeout)
		_, err = w.db.Exec(wctx, upsertProxyBindingsSQL, args...)
		cancel()
		if err == nil {
			w.written.Add(int64(len(args[0].([]string))))
			w.count("ok")
			return
		}
		if attempt < bindingWriteAttempts {
			w.count("retry")
			if !sleepContext(ctx, w.retryDelay*time.Duration(attempt)) {
				break
			}
		}
	}
	n := len(args[0].([]string))
	w.dropped.Add(int64(n))
	w.count("error")
	w.logger.Error("persist proxy bindings failed", slog.Int("bindings", n), slog.Any("error", err))
}

func (w *proxyBindingWriter) count(result string) {
	if w.metrics != nil {
		w.metrics.DBWriteBatches.WithLabelValues(bindingWriterName, result).Inc()
	}
}

// bindingArgs builds the unnest arguments of upsertProxyBindingsSQL, sorted by
// identity ID so that concurrent writers lock rows in the same order.
func bindingArgs(pending map[string]scheduler.ProxyBinding) []any {
	ids := make([]string, 0, len(pending))
	for id := range pending {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	identityIDs := make([]string, len(ids))
	proxyIDs := make([]string, len(ids))
	boundAt := make([]time.Time, len(ids))
	days := make([]string, len(ids))
	rebinds := make([]int32, len(ids))
	for i, id := range ids {
		b := pending[id]
		at := b.At
		if at.IsZero() {
			at = time.Now()
		}
		identityIDs[i] = id
		proxyIDs[i] = b.ProxyID
		boundAt[i] = at.UTC()
		days[i] = rebindDay(b.RebindDay, at)
		rebinds[i] = int32(max(0, min(b.RebindsToday, 1<<30)))
	}
	return []any{identityIDs, proxyIDs, boundAt, days, rebinds}
}

// rebindDay converts the hot-state "YYYYMMDD" day to an ISO date, falling back
// to the UTC day of at when the value is malformed.
func rebindDay(day string, at time.Time) string {
	if t, err := time.Parse("20060102", strings.TrimSpace(day)); err == nil {
		return t.Format(time.DateOnly)
	}
	return at.UTC().Format(time.DateOnly)
}
