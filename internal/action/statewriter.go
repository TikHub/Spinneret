package action

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/TikHub/Spinneret/internal/observability"
)

// StateWriter defaults (spec §6.6).
const (
	stateWriterName          = "state_events"
	defaultStateQueueSize    = 100_000
	defaultStateFlushEvery   = 200 * time.Millisecond
	defaultStateBatchSize    = 500
	defaultStateMaxAttempts  = 5
	defaultStateRetryBackoff = 100 * time.Millisecond
	maxStateRetryBackoff     = 2 * time.Second
	stateWriteTimeout        = 10 * time.Second
	stateShutdownTimeout     = 10 * time.Second
	// minUnavailableBackoff and maxUnavailableBackoff bound the wait between
	// flushes while PostgreSQL is unavailable (doubling, reset on success).
	minUnavailableBackoff = 200 * time.Millisecond
	maxUnavailableBackoff = 5 * time.Second
	// maxEnqueueWait bounds how long Enqueue (and the executor) waits for
	// queue space for a lifecycle change before dropping it.
	maxEnqueueWait = time.Minute
	dropLogEvery   = 1000
)

// errStateWriterClosed is returned when a change is enqueued after Run ended.
var errStateWriterClosed = errors.New("state writer is stopped")

// StateWriter persists state changes to PostgreSQL asynchronously: a bounded
// queue flushed every 200 ms or 500 changes. Subject rows are updated only
// when the change is newer than their last state change, and all changes are
// appended to state_events (idempotently: a change is inserted at most once
// per ID). It is safe for concurrent use.
//
// Failures are handled by class:
//   - PostgreSQL unavailable (connection errors, timeouts, SQLSTATE classes
//     08, 53, 57 except 57014, and 58): the batch is kept and retried by Run
//     with an exponential backoff (200 ms doubling to 5 s) until it is
//     written; nothing is dropped while the database is down.
//   - Data errors (SQLSTATE classes 22 and 23, 54000): the batch is split
//     until the rejected changes are isolated, and only those are dropped.
//   - Anything else is retried up to 5 times and then dropped.
//
// The queue holds at most 100 000 changes. When it is full, the oldest
// changes that do not update a subject row (cooldown and shadow events) are
// spilled first; lifecycle changes are never spilled, and enqueueing one
// waits (backpressure) until a flush frees space, for at most one minute.
// Spilled and dropped changes are counted (Dropped, Spilled and the
// spinneret_state_writer_* metrics).
type StateWriter struct {
	pool    *pgxpool.Pool
	metrics *observability.Metrics
	logger  *slog.Logger

	kick chan struct{}

	flushEvery   time.Duration
	batchSize    int
	maxAttempts  int
	retryBackoff time.Duration

	// mu guards the queue, the space signal and closed.
	mu        sync.Mutex
	capacity  int
	lifecycle fifo[StateChange] // changes that update a subject row
	events    fifo[StateChange] // everything else (spillable)
	// space is closed (and replaced) when a drain frees queue space while
	// enqueuers wait.
	space   chan struct{}
	waiters int
	closed  bool

	// flushMu serializes flushes and guards carry.
	flushMu sync.Mutex
	// carry is a batch whose write was interrupted by cancellation or by
	// PostgreSQL being unavailable; the next Flush writes it first.
	carry   []StateChange
	carried atomic.Int64
	queued  atomic.Int64
	dropped atomic.Int64
	spilled atomic.Int64
	written atomic.Int64
}

// NewStateWriter creates a writer. metrics and logger may be nil.
func NewStateWriter(pool *pgxpool.Pool, metrics *observability.Metrics, logger *slog.Logger) *StateWriter {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	w := &StateWriter{
		pool:         pool,
		metrics:      metrics,
		logger:       logger.With("component", "action.state_writer"),
		kick:         make(chan struct{}, 1),
		flushEvery:   defaultStateFlushEvery,
		batchSize:    defaultStateBatchSize,
		maxAttempts:  defaultStateMaxAttempts,
		retryBackoff: defaultStateRetryBackoff,
		capacity:     defaultStateQueueSize,
		space:        make(chan struct{}),
	}
	w.registerMetrics()
	return w
}

// registerMetrics exposes the queue gauges and loss counters on the metrics
// registry (ignored when metrics is nil or another writer registered them).
func (w *StateWriter) registerMetrics() {
	if w.metrics == nil || w.metrics.Registry == nil {
		return
	}
	collectors := []prometheus.Collector{
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: observability.MetricsNamespace, Name: "state_writer_pending_changes",
			Help: "State changes queued or kept for retry by the state writer.",
		}, func() float64 { return float64(w.Pending()) }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{
			Namespace: observability.MetricsNamespace, Name: "state_writer_dropped_changes_total",
			Help: "State changes discarded by the state writer (spilled, rejected or not enqueued in time).",
		}, func() float64 { return float64(w.Dropped()) }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{
			Namespace: observability.MetricsNamespace, Name: "state_writer_spilled_changes_total",
			Help: "Non-lifecycle state events discarded because the state writer queue was full.",
		}, func() float64 { return float64(w.Spilled()) }),
	}
	for _, c := range collectors {
		if err := w.metrics.Registry.Register(c); err != nil {
			var already prometheus.AlreadyRegisteredError
			if !errors.As(err, &already) {
				w.logger.Warn("register state writer metric", "error", err)
			}
		}
	}
}

// Enqueue queues a change. Changes that do not update a subject row never
// block: when the queue is full the oldest such change is spilled. A
// lifecycle change waits for queue space for at most one minute and is
// dropped (and counted) when none frees up.
func (w *StateWriter) Enqueue(c StateChange) {
	_ = w.enqueue(context.Background(), c, maxEnqueueWait)
}

// EnqueueContext queues a change like Enqueue, waiting for queue space for a
// lifecycle change until ctx is done. It returns an error when the change was
// dropped (ctx done or the writer stopped with a full queue); spilling an
// older non-lifecycle change is not an error.
func (w *StateWriter) EnqueueContext(ctx context.Context, c StateChange) error {
	return w.enqueue(ctx, c, 0)
}

// enqueue implements Enqueue and EnqueueContext; maxWait > 0 additionally
// bounds the wait for space (the timer is only created when waiting).
func (w *StateWriter) enqueue(ctx context.Context, c StateChange, maxWait time.Duration) error {
	c.normalize(time.Now())
	lifecycle := isLifecycleChange(c)
	var deadline <-chan time.Time
	for {
		w.mu.Lock()
		if w.lifecycle.len()+w.events.len() < w.capacity {
			w.push(c, lifecycle)
			w.mu.Unlock()
			return nil
		}
		if w.events.len() > 0 {
			// Spill the oldest change that does not update a subject row.
			w.events.pop()
			w.push(c, lifecycle)
			w.mu.Unlock()
			w.countLoss(true, "state change queue full, spilling the oldest non-lifecycle changes")
			return nil
		}
		if !lifecycle {
			w.mu.Unlock()
			w.countLoss(true, "state change queue full of lifecycle changes, spilling non-lifecycle change")
			return nil
		}
		if w.closed {
			w.mu.Unlock()
			w.countLoss(false, "state writer stopped with a full queue, dropping lifecycle change")
			return errStateWriterClosed
		}
		wait := w.space
		w.waiters++
		w.mu.Unlock()
		w.signalFlush()
		if maxWait > 0 && deadline == nil {
			timer := time.NewTimer(maxWait)
			defer timer.Stop()
			deadline = timer.C
		}
		var err error
		select {
		case <-wait:
		case <-ctx.Done():
			err = ctx.Err()
		case <-deadline:
			err = context.DeadlineExceeded
		}
		w.mu.Lock()
		w.waiters--
		w.mu.Unlock()
		if err != nil {
			w.countLoss(false, "state change queue full, dropping lifecycle change")
			return fmt.Errorf("enqueue state change: %w", err)
		}
	}
}

// push appends c to its queue and kicks a flush when a batch is ready. The
// caller holds mu.
func (w *StateWriter) push(c StateChange, lifecycle bool) {
	if lifecycle {
		w.lifecycle.push(c)
	} else {
		w.events.push(c)
	}
	n := w.lifecycle.len() + w.events.len()
	w.queued.Store(int64(n))
	if n >= w.batchSize {
		w.signalFlush()
	}
}

func (w *StateWriter) signalFlush() {
	select {
	case w.kick <- struct{}{}:
	default:
	}
}

// countLoss counts one discarded change and logs every dropLogEvery losses.
func (w *StateWriter) countLoss(spill bool, msg string) {
	n := w.dropped.Add(1)
	if spill {
		w.spilled.Add(1)
	}
	if n%dropLogEvery == 1 {
		w.logger.Warn(msg, "dropped_total", n, "spilled_total", w.spilled.Load())
	}
}

// isLifecycleChange reports whether c updates a subject row (it is never
// spilled).
func isLifecycleChange(c StateChange) bool {
	return c.UpdateSubject && !c.Shadow
}

// Dropped returns the number of changes discarded: spilled from a full queue,
// not enqueued in time, or rejected by PostgreSQL.
func (w *StateWriter) Dropped() int64 { return w.dropped.Load() }

// Spilled returns the number of non-lifecycle changes discarded because the
// queue was full (included in Dropped).
func (w *StateWriter) Spilled() int64 { return w.spilled.Load() }

// Written returns the number of changes persisted so far.
func (w *StateWriter) Written() int64 { return w.written.Load() }

// Pending returns the number of queued changes, including a batch kept for
// the next flush after an interrupted or unavailable one.
func (w *StateWriter) Pending() int { return int(w.queued.Load() + w.carried.Load()) }

// Run flushes the queue periodically until ctx is canceled and then drains it
// with a bounded shutdown timeout. While PostgreSQL is unavailable it retries
// with an exponential backoff instead of the flush interval.
func (w *StateWriter) Run(ctx context.Context) error {
	w.open()
	defer w.close()
	timer := time.NewTimer(w.flushEvery)
	defer timer.Stop()
	var backoff time.Duration
	for {
		kick := w.kick
		if backoff > 0 {
			kick = nil // a full queue must not bypass the backoff
		}
		select {
		case <-ctx.Done():
			w.shutdownFlush(ctx)
			return nil
		case <-timer.C:
		case <-kick:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}
		if err := w.Flush(ctx); err != nil && ctx.Err() == nil {
			w.logger.Error("state change flush failed", "error", err, "pending", w.Pending())
		}
		backoff = w.nextBackoff(backoff)
		wait := w.flushEvery
		if backoff > 0 {
			wait = backoff
		}
		timer.Reset(wait)
	}
}

// nextBackoff returns the wait before the next flush attempt: zero after a
// complete flush, otherwise the previous backoff doubled within
// [minUnavailableBackoff, maxUnavailableBackoff].
func (w *StateWriter) nextBackoff(prev time.Duration) time.Duration {
	if w.carried.Load() == 0 {
		return 0
	}
	return min(max(prev*2, minUnavailableBackoff), maxUnavailableBackoff)
}

// shutdownFlush drains the queue after Run's context ended, retrying while
// PostgreSQL is unavailable, bounded by stateShutdownTimeout.
func (w *StateWriter) shutdownFlush(ctx context.Context) {
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stateShutdownTimeout)
	defer cancel()
	var backoff time.Duration
	for {
		err := w.Flush(sctx)
		if w.Pending() == 0 {
			return
		}
		backoff = w.nextBackoff(backoff)
		if backoff == 0 {
			backoff = minUnavailableBackoff
		}
		timer := time.NewTimer(backoff)
		select {
		case <-sctx.Done():
			timer.Stop()
			w.logger.Error("final state change flush failed", "error", errors.Join(err, sctx.Err()), "pending", w.Pending())
			return
		case <-timer.C:
		}
	}
}

// open marks the writer running (Run may be restarted by a supervisor).
func (w *StateWriter) open() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = false
}

// close marks the writer stopped and releases enqueuers waiting for space.
func (w *StateWriter) close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	close(w.space)
	w.space = make(chan struct{})
}

// Flush writes every queued change. A write in progress is never interrupted
// by ctx; when ctx is canceled Flush stops between batches or retry attempts
// and keeps an unwritten batch for the next Flush. When PostgreSQL is
// unavailable the batch is kept as well and Flush returns the error; queued
// changes stay queued. Changes rejected by PostgreSQL (data errors) and
// batches failing for other reasons after all retries are dropped and
// counted; the first such error is returned.
func (w *StateWriter) Flush(ctx context.Context) error {
	w.flushMu.Lock()
	defer w.flushMu.Unlock()
	var firstErr error
	for {
		if err := ctx.Err(); err != nil {
			return errors.Join(firstErr, err)
		}
		batch := w.takeCarry()
		if len(batch) == 0 {
			batch = w.drain()
		}
		if len(batch) == 0 {
			return firstErr
		}
		err := w.writeWithRetry(ctx, batch)
		switch {
		case err == nil:
		case ctx.Err() != nil || isStateDBUnavailable(err):
			w.keepCarry(batch)
			return errors.Join(firstErr, err)
		case isStateDataError(err):
			if serr := w.salvage(ctx, batch); serr != nil {
				return errors.Join(firstErr, err, serr)
			}
			if firstErr == nil {
				firstErr = err
			}
		default:
			w.dropBatch(batch, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
}

// salvage writes a batch rejected with a data error by splitting it until
// the rejected changes are isolated; only those are dropped. When
// PostgreSQL becomes unavailable (or ctx is canceled) the unwritten rest is
// kept for the next Flush and the error is returned. The caller holds flushMu.
func (w *StateWriter) salvage(ctx context.Context, batch []StateChange) error {
	stack := [][]StateChange{batch}
	for len(stack) > 0 {
		part := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if len(part) > 1 {
			mid := len(part) / 2
			// Halves are pushed in reverse so the first half is written first.
			if err := ctx.Err(); err != nil {
				w.keepCarry(flatten(append(stack, part)))
				return err
			}
			if err := w.writeOnce(ctx, part[:mid]); err != nil {
				if isStateDBUnavailable(err) {
					w.keepCarry(flatten(append(stack, part)))
					return err
				}
				stack = append(stack, part[mid:], part[:mid])
				continue
			}
			stack = append(stack, part[mid:])
			continue
		}
		if err := w.writeOnce(ctx, part); err != nil {
			if isStateDBUnavailable(err) {
				w.keepCarry(flatten(append(stack, part)))
				return err
			}
			w.dropBatch(part, err)
		}
	}
	return nil
}

// flatten concatenates the parts of a salvage stack in write order (the
// stack's top is written first).
func flatten(stack [][]StateChange) []StateChange {
	var out []StateChange
	for i := len(stack) - 1; i >= 0; i-- {
		out = append(out, stack[i]...)
	}
	return out
}

// writeOnce writes a batch with a single attempt and records the outcome.
func (w *StateWriter) writeOnce(ctx context.Context, batch []StateChange) error {
	if err := w.writeBatch(ctx, batch); err != nil {
		w.observe("error")
		return err
	}
	w.written.Add(int64(len(batch)))
	w.observe("ok")
	return nil
}

// dropBatch counts and logs a discarded batch.
func (w *StateWriter) dropBatch(batch []StateChange, err error) {
	w.dropped.Add(int64(len(batch)))
	w.observe("dropped")
	w.logger.Error("dropping state change batch", "error", err, "changes", len(batch))
}

// keepCarry keeps batch for the next Flush. The caller holds flushMu.
func (w *StateWriter) keepCarry(batch []StateChange) {
	w.carry = batch
	w.carried.Store(int64(len(batch)))
}

// takeCarry returns and clears the batch kept by an interrupted flush. The
// caller holds flushMu.
func (w *StateWriter) takeCarry() []StateChange {
	batch := w.carry
	w.carry = nil
	w.carried.Store(0)
	return batch
}

// drain takes up to batchSize changes, lifecycle changes first, and wakes
// enqueuers waiting for space.
func (w *StateWriter) drain() []StateChange {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := min(w.batchSize, w.lifecycle.len()+w.events.len())
	if n == 0 {
		return nil
	}
	batch := make([]StateChange, 0, n)
	for len(batch) < n && w.lifecycle.len() > 0 {
		batch = append(batch, w.lifecycle.pop())
	}
	for len(batch) < n && w.events.len() > 0 {
		batch = append(batch, w.events.pop())
	}
	w.queued.Store(int64(w.lifecycle.len() + w.events.len()))
	if w.waiters > 0 {
		close(w.space)
		w.space = make(chan struct{})
	}
	return batch
}

// writeWithRetry writes a batch, retrying failures up to maxAttempts with a
// capped exponential backoff. It returns right away when PostgreSQL is
// unavailable (Run backs off) or the batch is rejected with a data error
// other than a missing partition (Flush salvages it).
func (w *StateWriter) writeWithRetry(ctx context.Context, batch []StateChange) error {
	backoff := w.retryBackoff
	var err error
	for attempt := 1; attempt <= w.maxAttempts; attempt++ {
		if cerr := ctx.Err(); cerr != nil {
			return errors.Join(err, cerr)
		}
		if err = w.writeOnce(ctx, batch); err == nil {
			return nil
		}
		if isStateDBUnavailable(err) || (isStateDataError(err) && !isMissingPartition(err)) {
			return err
		}
		if attempt == w.maxAttempts {
			break
		}
		w.logger.Warn("state change batch failed, retrying", "error", err, "attempt", attempt, "changes", len(batch))
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(err, ctx.Err())
		case <-timer.C:
		}
		backoff = min(backoff*2, maxStateRetryBackoff)
	}
	return fmt.Errorf("write %d state changes after %d attempts: %w", len(batch), w.maxAttempts, err)
}

func (w *StateWriter) observe(result string) {
	if w.metrics != nil {
		w.metrics.DBWriteBatches.WithLabelValues(stateWriterName, result).Inc()
	}
}

// writeBatch resolves subject IDs, updates subject rows and appends the state
// events in one transaction. The write is bounded by stateWriteTimeout but not
// canceled with ctx: interrupting a commit could leave the outcome unknown.
// Events are inserted with ON CONFLICT DO NOTHING, so retrying a batch whose
// commit outcome was unknown never duplicates them.
func (w *StateWriter) writeBatch(ctx context.Context, batch []StateChange) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stateWriteTimeout)
	defer cancel()
	changes, err := w.resolveSubjects(ctx, batch)
	if err != nil {
		return err
	}
	tx, err := w.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("begin state write: %w", err)
	}
	defer func() {
		rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer rcancel()
		_ = tx.Rollback(rctx)
	}()
	if err := updateSubjects(ctx, tx, changes); err != nil {
		return err
	}
	if err := insertStateEventsOnce(ctx, tx, changes); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit state write: %w", err)
	}
	return nil
}

// resolveSubjects fills missing subject IDs from their hkeys. Changes whose
// subject no longer exists are dropped. The input slice is not modified.
func (w *StateWriter) resolveSubjects(ctx context.Context, batch []StateChange) ([]StateChange, error) {
	missing := map[string][]int64{}
	for _, c := range batch {
		if c.SubjectID == "" && c.SubjectKey > 0 {
			missing[c.SubjectKind] = append(missing[c.SubjectKind], c.SubjectKey)
		}
	}
	if len(missing) == 0 {
		return batch, nil
	}
	resolved := map[string]map[int64]string{}
	for kind, keys := range missing {
		ids, err := lookupIDsByKeys(ctx, w.pool, kind, keys)
		if err != nil {
			return nil, err
		}
		resolved[kind] = ids
	}
	out := make([]StateChange, 0, len(batch))
	for _, c := range batch {
		if c.SubjectID == "" {
			c.SubjectID = resolved[c.SubjectKind][c.SubjectKey]
			if c.SubjectID == "" {
				w.logger.Debug("dropping state change for unknown subject", "kind", c.SubjectKind, "key", c.SubjectKey)
				continue
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// fifo is a slice-backed first-in first-out queue. It is not safe for
// concurrent use.
type fifo[T any] struct {
	items []T
	head  int
}

func (q *fifo[T]) len() int { return len(q.items) - q.head }

func (q *fifo[T]) push(v T) { q.items = append(q.items, v) }

// pop removes and returns the oldest item; the queue must not be empty.
func (q *fifo[T]) pop() T {
	var zero T
	v := q.items[q.head]
	q.items[q.head] = zero
	q.head++
	switch {
	case q.head == len(q.items):
		q.items, q.head = q.items[:0], 0
	case q.head > 1024 && q.head*2 > len(q.items):
		n := copy(q.items, q.items[q.head:])
		clear(q.items[n:])
		q.items, q.head = q.items[:n], 0
	}
	return v
}
