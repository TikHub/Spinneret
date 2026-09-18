package spinneret

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Default reporter tuning.
const (
	DefaultFlushInterval         = 200 * time.Millisecond
	DefaultReportBatchSize       = 100
	DefaultReportQueueSize       = 10_000
	DefaultReporterBackoff       = 500 * time.Millisecond
	DefaultReporterMaxBackoff    = 30 * time.Second
	DefaultReporterCloseTimeout  = 5 * time.Second
	reporterDropLogInterval      = 10 * time.Second
	reporterCloseGrace           = time.Second
	reporterRejectedLogMaxDetail = 16
)

// ReporterOptions tunes a [Reporter]. Zero values select the defaults.
type ReporterOptions struct {
	// FlushInterval is the longest time a report waits before a send (default 200ms).
	FlushInterval time.Duration
	// BatchSize is the queue length that triggers an immediate send (default 100).
	BatchSize int
	// MaxBatchSize is the maximum number of reports per Report call (default and max 500).
	MaxBatchSize int
	// MaxQueueSize bounds the queue; the oldest reports are dropped beyond it (default 10000).
	MaxQueueSize int
	// InitialBackoff is the first delay after a failed delivery (default 500ms).
	InitialBackoff time.Duration
	// MaxBackoff caps the delay between failed deliveries (default 30s).
	MaxBackoff time.Duration
	// CloseTimeout bounds Close when its context has no deadline (default 5s).
	CloseTimeout time.Duration
	// OnRejected is invoked (on the reporter goroutine) for every report the
	// server rejected. It must not block for long nor call Flush or Close.
	OnRejected func(*RejectedReport)
}

func (o ReporterOptions) normalized() (ReporterOptions, error) {
	if o.FlushInterval < 0 || o.BatchSize < 0 || o.MaxBatchSize < 0 || o.MaxQueueSize < 0 ||
		o.InitialBackoff < 0 || o.MaxBackoff < 0 || o.CloseTimeout < 0 {
		return o, errInvalidOptions("reporter options must not be negative")
	}
	if o.FlushInterval == 0 {
		o.FlushInterval = DefaultFlushInterval
	}
	if o.MaxBatchSize == 0 {
		o.MaxBatchSize = MaxReportsPerCall
	}
	if o.MaxBatchSize > MaxReportsPerCall {
		return o, errInvalidOptions("reporter MaxBatchSize must be within 1..%d", MaxReportsPerCall)
	}
	if o.BatchSize == 0 {
		o.BatchSize = min(DefaultReportBatchSize, o.MaxBatchSize)
	}
	if o.BatchSize > o.MaxBatchSize {
		return o, errInvalidOptions("reporter BatchSize must be within 1..MaxBatchSize")
	}
	if o.MaxQueueSize == 0 {
		o.MaxQueueSize = max(DefaultReportQueueSize, o.MaxBatchSize)
	}
	if o.MaxQueueSize < o.MaxBatchSize {
		return o, errInvalidOptions("reporter MaxQueueSize must be >= MaxBatchSize")
	}
	if o.InitialBackoff == 0 {
		o.InitialBackoff = DefaultReporterBackoff
	}
	if o.MaxBackoff == 0 {
		o.MaxBackoff = DefaultReporterMaxBackoff
	}
	o.MaxBackoff = max(o.MaxBackoff, o.InitialBackoff)
	if o.CloseTimeout == 0 {
		o.CloseTimeout = DefaultReporterCloseTimeout
	}
	return o, nil
}

// ReporterStats are the counters of a reporter since it was created.
type ReporterStats struct {
	// Submitted counts reports handed to Submit.
	Submitted int64
	// Sent counts reports included in successful Report calls.
	Sent int64
	// Accepted counts reports accepted by the server.
	Accepted int64
	// Duplicated counts reports ignored by the server as duplicates.
	Duplicated int64
	// Rejected counts reports rejected by the server (dropped permanently).
	Rejected int64
	// Dropped counts reports dropped locally: queue overflow, permanent
	// delivery errors and reports left when Close ran out of time.
	Dropped int64
	// FailedSends counts failed Report calls.
	FailedSends int64
	// Queued is the number of reports waiting in the queue.
	Queued int
}

// SendFunc delivers one batch of reports.
type SendFunc func(ctx context.Context, reports []*Report) (*ReportResponse, error)

type queuedReport struct {
	report     *Report
	enqueuedAt time.Time
}

// Reporter batches reports and delivers them from a background goroutine.
//
// A batch is sent when BatchSize reports are queued or the oldest report
// waited FlushInterval, with at most MaxBatchSize reports per call. Failures
// for which [IsRetryable] is true (transport errors, unavailable, internal,
// ...) are retried with jittered exponential backoff; other failures and
// reports rejected by the server are dropped. The queue is bounded: when it is
// full the oldest reports are dropped and counted in [ReporterStats.Dropped].
//
// Submit never blocks on the network. Call Close (or [Client.Close]) to
// deliver the remaining reports.
type Reporter struct {
	send   SendFunc
	opts   ReporterOptions
	logger *slog.Logger
	rnd    func() float64

	// ctx aborts in-flight sends and backoff when Close runs out of time.
	ctx    context.Context
	cancel context.CancelFunc
	wake   chan struct{} // new work, flush requests and close (capacity 1)
	closed chan struct{} // closed by Close
	done   chan struct{} // closed when the worker exits

	mu           sync.Mutex
	queue        []queuedReport
	inflight     int
	flushWaiters int
	failures     int
	closing      bool
	started      bool
	changed      chan struct{} // closed and replaced whenever deliveries progress
	stats        ReporterStats
	lastDropLog  time.Time
	dropsSince   int64
}

// NewReporter creates a reporter delivering batches with send. A nil logger
// uses slog.Default(). Most nodes use [Client.Reporter] instead.
func NewReporter(send SendFunc, opts ReporterOptions, logger *slog.Logger) (*Reporter, error) {
	if send == nil {
		return nil, errInvalidOptions("reporter send function must not be nil")
	}
	normalized, err := opts.normalized()
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	return newReporter(send, normalized, logger), nil
}

func newReporter(send SendFunc, opts ReporterOptions, logger *slog.Logger) *Reporter {
	ctx, cancel := context.WithCancel(context.Background())
	return &Reporter{
		send:    send,
		opts:    opts,
		logger:  logger,
		rnd:     jitter,
		ctx:     ctx,
		cancel:  cancel,
		wake:    make(chan struct{}, 1),
		closed:  make(chan struct{}),
		done:    make(chan struct{}),
		changed: make(chan struct{}),
	}
}

// Options returns the normalized options of the reporter.
func (r *Reporter) Options() ReporterOptions { return r.opts }

// Stats returns a snapshot of the delivery counters.
func (r *Reporter) Stats() ReporterStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.stats
	s.Queued = len(r.queue)
	return s
}

// Closed reports whether Close has been called.
func (r *Reporter) Closed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closing
}

// Submit queues a report for delivery. The reporter takes ownership of the
// report and fills the fields the server requires: an empty ReportId becomes
// a random UUID (so that retried deliveries are deduplicated), a missing
// FinishedAt becomes now and a missing StartedAt becomes FinishedAt minus
// LatencyMs. It fails with reason reporter_closed after Close.
func (r *Reporter) Submit(report *Report) error {
	if report == nil {
		return newError(connect.CodeInvalidArgument, ReasonInvalidArgument, "report must not be nil")
	}
	r.mu.Lock()
	if r.closing {
		r.mu.Unlock()
		return errReporterClosed()
	}
	if report.GetReportId() == "" {
		report.ReportId = NewReportID()
	}
	if report.GetFinishedAt() == nil {
		report.FinishedAt = timestamppb.Now()
	}
	if report.GetStartedAt() == nil {
		latency := time.Duration(max(report.GetLatencyMs(), 0)) * time.Millisecond
		report.StartedAt = timestamppb.New(report.GetFinishedAt().AsTime().Add(-latency))
	}
	r.stats.Submitted++
	r.queue = append(r.queue, queuedReport{report: report, enqueuedAt: time.Now()})
	r.trimLocked()
	start := !r.started
	r.started = true
	notify := len(r.queue) == 1 || len(r.queue) >= r.opts.BatchSize
	r.mu.Unlock()
	if start {
		go r.run()
	}
	if notify {
		r.signal()
	}
	return nil
}

// Flush sends every queued report now and waits until the queue is drained
// or ctx ends. Failed deliveries keep being retried while Flush waits.
func (r *Reporter) Flush(ctx context.Context) error {
	r.mu.Lock()
	if len(r.queue) == 0 && r.inflight == 0 {
		r.mu.Unlock()
		return nil
	}
	r.flushWaiters++
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.flushWaiters--
		r.mu.Unlock()
	}()
	r.signal()
	for {
		r.mu.Lock()
		if len(r.queue) == 0 && r.inflight == 0 {
			r.mu.Unlock()
			return nil
		}
		changed := r.changed
		r.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return fmt.Errorf("spinneret: flush reports: %w", ctx.Err())
		}
	}
}

// Close delivers the queued reports and stops the reporter. It waits until
// the queue is empty or ctx ends (ctx without deadline: Options.CloseTimeout);
// reports still queued then are dropped and an error is returned. Close is
// idempotent.
func (r *Reporter) Close(ctx context.Context) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.opts.CloseTimeout)
		defer cancel()
	}
	r.mu.Lock()
	if !r.closing {
		r.closing = true
		close(r.closed)
	}
	started := r.started
	droppedBefore := r.stats.Dropped
	r.mu.Unlock()
	if !started {
		r.cancel()
		return nil
	}
	r.signal()
	select {
	case <-r.done:
	case <-ctx.Done():
		r.cancel()
		grace := time.NewTimer(reporterCloseGrace)
		defer grace.Stop()
		select {
		case <-r.done:
		case <-grace.C:
			r.logger.Warn("spinneret reporter did not stop in time")
		}
	}
	r.cancel()
	stats := r.Stats()
	if dropped := stats.Dropped - droppedBefore; dropped > 0 || stats.Queued > 0 {
		cause := ctx.Err()
		if cause == nil {
			cause = errReporterClosed()
		}
		return fmt.Errorf("spinneret: reporter dropped %d reports while closing (%d still queued): %w",
			dropped, stats.Queued, cause)
	}
	return nil
}

// closeNow closes a reporter that was never started.
func (r *Reporter) closeNow() {
	r.mu.Lock()
	if !r.closing {
		r.closing = true
		close(r.closed)
	}
	r.mu.Unlock()
	r.cancel()
}

func errReporterClosed() *Error {
	return newError(connect.CodeFailedPrecondition, ReasonReporterClosed, "reporter is closed")
}

func (r *Reporter) signal() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// broadcastLocked wakes Flush waiters.
func (r *Reporter) broadcastLocked() {
	close(r.changed)
	r.changed = make(chan struct{})
}

func (r *Reporter) run() {
	defer func() {
		r.mu.Lock()
		r.inflight = 0
		r.broadcastLocked()
		r.mu.Unlock()
		close(r.done)
	}()
	for {
		batch := r.nextBatch()
		if batch == nil {
			return
		}
		reports := make([]*Report, len(batch))
		for i, q := range batch {
			reports[i] = q.report
		}
		resp, err := r.send(r.ctx, reports)
		if err != nil {
			if delay, retry := r.recordFailure(batch, err); retry {
				r.backoff(delay)
			}
			continue
		}
		r.recordResponse(batch, resp)
	}
}

// nextBatch blocks until a batch is due and returns it, or returns nil when
// the reporter stops.
func (r *Reporter) nextBatch() []queuedReport {
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	for {
		r.mu.Lock()
		if r.closing {
			if len(r.queue) == 0 || r.ctx.Err() != nil {
				r.dropAllLocked("close timeout expired")
				r.mu.Unlock()
				return nil
			}
			batch := r.takeLocked()
			r.mu.Unlock()
			return batch
		}
		wait, pending := r.untilDueLocked()
		if pending && (wait == 0 || r.flushWaiters > 0) {
			batch := r.takeLocked()
			r.mu.Unlock()
			return batch
		}
		r.mu.Unlock()
		if !pending {
			<-r.wake
			continue
		}
		timer.Reset(wait)
		select {
		case <-r.wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// untilDueLocked returns the time until the oldest report is due and whether
// the queue holds reports.
func (r *Reporter) untilDueLocked() (time.Duration, bool) {
	if len(r.queue) == 0 {
		return 0, false
	}
	if len(r.queue) >= r.opts.BatchSize {
		return 0, true
	}
	return max(0, r.opts.FlushInterval-time.Since(r.queue[0].enqueuedAt)), true
}

func (r *Reporter) takeLocked() []queuedReport {
	n := min(len(r.queue), r.opts.MaxBatchSize)
	batch := make([]queuedReport, n)
	copy(batch, r.queue[:n])
	clear(r.queue[:n])
	r.queue = r.queue[n:]
	if len(r.queue) == 0 {
		r.queue = nil
	}
	r.inflight = n
	return batch
}

func (r *Reporter) trimLocked() {
	overflow := len(r.queue) - r.opts.MaxQueueSize
	if overflow <= 0 {
		return
	}
	clear(r.queue[:overflow])
	r.queue = r.queue[overflow:]
	r.stats.Dropped += int64(overflow)
	r.dropsSince += int64(overflow)
	if now := time.Now(); now.Sub(r.lastDropLog) >= reporterDropLogInterval {
		r.logger.Warn("spinneret report queue full, dropped oldest reports",
			slog.Int("max_queue_size", r.opts.MaxQueueSize),
			slog.Int64("dropped", r.dropsSince),
			slog.Int64("dropped_total", r.stats.Dropped))
		r.lastDropLog = now
		r.dropsSince = 0
	}
}

func (r *Reporter) dropAllLocked(why string) {
	if n := len(r.queue); n > 0 {
		clear(r.queue)
		r.queue = nil
		r.stats.Dropped += int64(n)
		r.logger.Warn("spinneret reporter dropped queued reports",
			slog.Int("count", n), slog.String("why", why))
	}
}

// recordFailure books a failed send and returns the backoff delay and whether
// the batch was put back for a retry.
func (r *Reporter) recordFailure(batch []queuedReport, err error) (time.Duration, bool) {
	r.mu.Lock()
	r.stats.FailedSends++
	r.inflight = 0
	retry := IsRetryable(err) && r.ctx.Err() == nil
	var delay time.Duration
	if retry {
		requeued := make([]queuedReport, 0, len(batch)+len(r.queue))
		requeued = append(requeued, batch...)
		r.queue = append(requeued, r.queue...)
		r.trimLocked()
		r.failures++
		delay = backoff{initial: r.opts.InitialBackoff, max: r.opts.MaxBackoff}.delay(r.failures-1, r.rnd)
		if hint := RetryAfterOf(err); hint > 0 {
			delay = max(delay, min(hint, r.opts.MaxBackoff))
		}
	} else {
		r.stats.Dropped += int64(len(batch))
		r.failures = 0
	}
	attempt := r.failures
	r.broadcastLocked()
	r.mu.Unlock()
	if retry {
		r.logger.Warn("spinneret report delivery failed, will retry",
			slog.Int("count", len(batch)),
			slog.Int("attempt", attempt),
			slog.Duration("delay", delay),
			slog.String("error", err.Error()))
	} else {
		r.logger.Error("spinneret report delivery failed permanently, dropped reports",
			slog.Int("count", len(batch)), slog.String("error", err.Error()))
	}
	return delay, retry
}

// backoff waits before a retry. Close interrupts a wait that started before
// it; the close deadline interrupts any wait.
func (r *Reporter) backoff(delay time.Duration) {
	r.mu.Lock()
	closing := r.closing
	r.mu.Unlock()
	closed := r.closed
	if closing {
		closed = nil // already closing: only the close deadline interrupts
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-closed:
	case <-r.ctx.Done():
	}
}

func (r *Reporter) recordResponse(batch []queuedReport, resp *ReportResponse) {
	rejected := resp.GetRejected()
	r.mu.Lock()
	r.failures = 0
	r.stats.Sent += int64(len(batch))
	r.stats.Accepted += int64(resp.GetAccepted())
	r.stats.Duplicated += int64(resp.GetDuplicated())
	r.stats.Rejected += int64(len(rejected))
	r.inflight = 0
	r.broadcastLocked()
	r.mu.Unlock()
	if len(rejected) == 0 {
		return
	}
	first := rejected[0]
	r.logger.Warn("spinneret server rejected reports",
		slog.Int("rejected", len(rejected)),
		slog.Int("batch", len(batch)),
		slog.String("first_report_id", first.GetReportId()),
		slog.String("first_reason", first.GetReason()),
		slog.String("first_message", first.GetMessage()))
	for i, rej := range rejected[1:] {
		if i >= reporterRejectedLogMaxDetail {
			break
		}
		r.logger.Debug("spinneret server rejected report",
			slog.String("report_id", rej.GetReportId()),
			slog.String("reason", rej.GetReason()),
			slog.String("message", rej.GetMessage()))
	}
	if r.opts.OnRejected == nil {
		return
	}
	for _, rej := range rejected {
		r.notifyRejected(rej)
	}
}

func (r *Reporter) notifyRejected(rej *RejectedReport) {
	defer func() {
		if p := recover(); p != nil {
			r.logger.Error("spinneret OnRejected callback panicked", slog.Any("panic", p))
		}
	}()
	r.opts.OnRejected(rej)
}
