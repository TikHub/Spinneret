package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"sync/atomic"
	"time"

	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// ReportEvent is one row of report_events: a node report after classification.
// Zero timestamps are stored as the Unix epoch, except EventTime, which
// defaults to the time the event was queued.
type ReportEvent struct {
	// EventTime is when the request finished; ReceivedAt when the report was
	// ingested; StartedAt when the request started.
	EventTime, ReceivedAt, StartedAt time.Time
	// Tenant, namespace and site identifiers plus the site/endpoint group names and client kind.
	TenantID, NamespaceID, SiteID, Site, EndpointGroup, Client string
	// Subjects and correlation identifiers of the leased request.
	IdentityID, IdentityType, ProxyID, LeaseID, ReportID, Node, TokenID string
	// URI (path) and HTTP method of the request.
	URI, Method string
	// HTTPStatus is the response status (0 = no response).
	HTTPStatus uint16
	// BusinessCode and ErrorKind are the raw node-reported signals.
	BusinessCode, ErrorKind string
	// Markers are the node-reported content markers (e.g. "empty_list").
	Markers []string
	// Outcome is the classified outcome, OutcomeHint the node hint, Blame the
	// blamed subject and Rule the matching signal rule name.
	Outcome, OutcomeHint, Blame, Rule string
	// LatencyMs is the request latency in milliseconds.
	LatencyMs uint32
	// ResponseBytes is the response body size.
	ResponseBytes uint64
	// Suppressed marks reports whose health/actions were skipped, Late reports
	// received after lease end, Probe reports of half-open probe leases.
	Suppressed, Late, Probe bool
}

// LeaseEvent is one row of lease_events: a lease lifecycle event.
type LeaseEvent struct {
	// EventTime is when the event happened (zero = time queued).
	EventTime time.Time
	// Tenant, namespace and site identifiers plus the site/endpoint group names and client kind.
	TenantID, NamespaceID, SiteID, Site, EndpointGroup, Client string
	// Subjects and correlation identifiers of the lease.
	IdentityID, ProxyID, LeaseID, Node, TokenID string
	// Event is one of acquired|renewed|released|expired|abandoned|rejected.
	Event string
	// Result is the operation result (e.g. ok, exhausted, circuit_open).
	Result string
	// DurationUs is the operation duration in microseconds.
	DurationUs uint32
	// Probe marks half-open probe leases; Sticky leases reused through a session key.
	Probe, Sticky bool
}

const (
	// DefaultBufferSize is the capacity of each in-memory event queue.
	DefaultBufferSize = 100_000
	// DefaultFlushEvery is used when NewWriter receives a non-positive flush interval.
	DefaultFlushEvery = time.Second
	// DefaultMaxBatch is used when NewWriter receives a non-positive batch size.
	DefaultMaxBatch = 10_000

	maxRetries      = 3
	baseBackoff     = 250 * time.Millisecond
	sendTimeout     = 30 * time.Second
	shutdownTimeout = 10 * time.Second
)

// ErrWriterRunning is returned by Run when another Run call is already active.
var ErrWriterRunning = errors.New("clickhouse: writer is already running")

var (
	// minEventTime is the Unix epoch; earlier (and zero) timestamps are stored as the epoch.
	minEventTime = time.Unix(0, 0).UTC()
	// maxEventTime is the latest instant representable by the driver's DateTime64
	// encoding (Unix nanoseconds in an int64); later timestamps would wrap around.
	maxEventTime = time.Unix(0, math.MaxInt64).UTC().Truncate(time.Millisecond)
)

// Writer asynchronously batches events into ClickHouse. Add* never block: when
// a queue is full the event is dropped and counted. Run flushes every
// flushEvery or as soon as maxBatch rows are queued; a failed batch is retried
// up to three times with exponential backoff and then dropped with an error
// log. A Writer with a nil connection, and a nil *Writer, discard everything.
type Writer struct {
	conn       chdriver.Conn
	logger     *slog.Logger
	flushEvery time.Duration
	maxBatch   int
	backoff    time.Duration
	// shutdownTimeout bounds the final flush.
	shutdownTimeout time.Duration

	reportInsert string
	leaseInsert  string

	reports chan ReportEvent
	leases  chan LeaseEvent

	running       atomic.Bool
	dropped       atomic.Int64
	written       atomic.Int64
	reportedDrops int64 // accessed only by the active Run
}

// NewWriter returns a writer for conn. conn may be nil, which yields a no-op
// writer. Non-positive flushEvery/maxBatch fall back to DefaultFlushEvery and
// DefaultMaxBatch; a nil logger discards logs.
func NewWriter(conn chdriver.Conn, logger *slog.Logger, flushEvery time.Duration, maxBatch int) *Writer {
	return newWriter(conn, logger, flushEvery, maxBatch, DefaultBufferSize)
}

func newWriter(conn chdriver.Conn, logger *slog.Logger, flushEvery time.Duration, maxBatch, bufferSize int) *Writer {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if flushEvery <= 0 {
		flushEvery = DefaultFlushEvery
	}
	if maxBatch <= 0 {
		maxBatch = DefaultMaxBatch
	}
	if bufferSize <= 0 {
		bufferSize = DefaultBufferSize
	}
	w := &Writer{
		conn:       conn,
		logger:     logger.With(slog.String("component", "clickhouse_writer")),
		flushEvery: flushEvery,
		maxBatch:   maxBatch,
		backoff:    baseBackoff,

		shutdownTimeout: shutdownTimeout,

		reportInsert: reportEventsSpec().insertSQL(),
		leaseInsert:  leaseEventsSpec().insertSQL(),
	}
	if conn != nil {
		w.reports = make(chan ReportEvent, bufferSize)
		w.leases = make(chan LeaseEvent, bufferSize)
	}
	return w
}

// AddReport queues a report event without blocking. A zero EventTime is
// replaced with the current time. The Markers slice is copied, so the caller
// may reuse or modify its backing array after the call. The event is dropped
// (and counted) when the queue is full; with a nil connection it is silently
// discarded.
func (w *Writer) AddReport(ev ReportEvent) {
	if w == nil || w.conn == nil {
		return
	}
	if ev.EventTime.IsZero() {
		ev.EventTime = time.Now()
	}
	// The event is written asynchronously by Run; sharing the caller's backing
	// array would be a data race and could persist later modifications.
	ev.Markers = slices.Clone(ev.Markers)
	select {
	case w.reports <- ev:
	default:
		w.dropped.Add(1)
	}
}

// AddLease queues a lease event without blocking, with the same semantics as AddReport.
func (w *Writer) AddLease(ev LeaseEvent) {
	if w == nil || w.conn == nil {
		return
	}
	if ev.EventTime.IsZero() {
		ev.EventTime = time.Now()
	}
	select {
	case w.leases <- ev:
	default:
		w.dropped.Add(1)
	}
}

// Dropped returns the number of events dropped because a queue was full or a
// batch failed permanently.
func (w *Writer) Dropped() int64 {
	if w == nil {
		return 0
	}
	return w.dropped.Load()
}

// Written returns the number of events successfully inserted.
func (w *Writer) Written() int64 {
	if w == nil {
		return 0
	}
	return w.written.Load()
}

// Run flushes queued events until ctx is done, then performs a final flush of
// everything still queued (bounded by a 10 s timeout) and returns nil. A
// concurrent second call returns ErrWriterRunning. With a nil connection (or a
// nil *Writer) it just waits for ctx.
func (w *Writer) Run(ctx context.Context) error {
	if w == nil || w.conn == nil {
		<-ctx.Done()
		return nil
	}
	if !w.running.CompareAndSwap(false, true) {
		return ErrWriterRunning
	}
	defer w.running.Store(false)

	ticker := time.NewTicker(w.flushEvery)
	defer ticker.Stop()

	reports := make([]ReportEvent, 0, w.maxBatch)
	leases := make([]LeaseEvent, 0, w.maxBatch)
	for {
		select {
		case <-ctx.Done():
			w.shutdown(ctx, reports, leases)
			return nil
		case ev := <-w.reports:
			reports = append(reports, ev)
			if len(reports) >= w.maxBatch {
				reports = w.flushReports(ctx, reports)
			}
		case ev := <-w.leases:
			leases = append(leases, ev)
			if len(leases) >= w.maxBatch {
				leases = w.flushLeases(ctx, leases)
			}
		case <-ticker.C:
			reports = w.flushReports(ctx, reports)
			leases = w.flushLeases(ctx, leases)
			w.logDrops()
		}
	}
}

// shutdown drains the events queued at cancellation time and flushes them with
// a detached context bounded by the shutdown timeout.
func (w *Writer) shutdown(ctx context.Context, reports []ReportEvent, leases []LeaseEvent) {
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), w.shutdownTimeout)
	defer cancel()

	// Drain only what is queued now so that producers that keep adding events
	// cannot prolong the shutdown indefinitely.
	for n := len(w.reports); n > 0; n-- {
		reports = append(reports, <-w.reports)
		if len(reports) >= w.maxBatch {
			reports = w.flushReports(fctx, reports)
		}
	}
	for n := len(w.leases); n > 0; n-- {
		leases = append(leases, <-w.leases)
		if len(leases) >= w.maxBatch {
			leases = w.flushLeases(fctx, leases)
		}
	}
	reports = w.flushReports(fctx, reports)
	leases = w.flushLeases(fctx, leases)
	// Anything left could not be written before the shutdown deadline.
	if n := len(reports) + len(leases); n > 0 {
		w.dropped.Add(int64(n))
		w.logger.Error("clickhouse events dropped at shutdown", slog.Int("rows", n))
	}
	w.logDrops()
}

// flushReports writes buf and returns the buffer to reuse. The rows are kept
// only when the write was interrupted by ctx cancellation, so that the final
// flush can retry them.
func (w *Writer) flushReports(ctx context.Context, buf []ReportEvent) []ReportEvent {
	if len(buf) == 0 {
		return buf
	}
	err := w.send(ctx, ReportEventsTable, len(buf), func(ctx context.Context) error {
		return w.insert(ctx, w.reportInsert, len(buf), func(b chdriver.Batch, i int) error {
			return appendReport(b, buf[i])
		})
	})
	if err != nil && ctx.Err() != nil {
		return buf
	}
	clear(buf)
	return buf[:0]
}

// flushLeases is flushReports for lease events.
func (w *Writer) flushLeases(ctx context.Context, buf []LeaseEvent) []LeaseEvent {
	if len(buf) == 0 {
		return buf
	}
	err := w.send(ctx, LeaseEventsTable, len(buf), func(ctx context.Context) error {
		return w.insert(ctx, w.leaseInsert, len(buf), func(b chdriver.Batch, i int) error {
			return appendLease(b, buf[i])
		})
	})
	if err != nil && ctx.Err() != nil {
		return buf
	}
	clear(buf)
	return buf[:0]
}

// send runs write with retries. On permanent failure (not caused by ctx) the
// rows are counted as dropped and an error is logged.
func (w *Writer) send(ctx context.Context, table string, rows int, write func(context.Context) error) error {
	var err error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(w.backoff << (attempt - 1))
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		if err = write(ctx); err == nil {
			w.written.Add(int64(rows))
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		w.logger.Warn("clickhouse batch insert failed",
			slog.String("table", table), slog.Int("rows", rows), slog.Int("attempt", attempt+1), slog.Any("error", err))
	}
	w.dropped.Add(int64(rows))
	w.logger.Error("clickhouse batch dropped after retries",
		slog.String("table", table), slog.Int("rows", rows), slog.Any("error", err))
	return err
}

// insert prepares a batch, appends rows via add and sends it.
func (w *Writer) insert(ctx context.Context, query string, rows int, add func(chdriver.Batch, int) error) error {
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	batch, err := w.conn.PrepareBatch(ctx, query)
	if err != nil {
		return fmt.Errorf("prepare batch: %w", err)
	}
	for i := 0; i < rows; i++ {
		if err := add(batch, i); err != nil {
			_ = batch.Abort()
			return fmt.Errorf("append row: %w", err)
		}
	}
	if err := batch.Send(); err != nil {
		if !batch.IsSent() {
			_ = batch.Abort()
		}
		return fmt.Errorf("send batch: %w", err)
	}
	return nil
}

// logDrops logs how many events were dropped since the previous call.
func (w *Writer) logDrops() {
	total := w.dropped.Load()
	if delta := total - w.reportedDrops; delta > 0 {
		w.reportedDrops = total
		w.logger.Warn("clickhouse events dropped", slog.Int64("dropped", delta), slog.Int64("dropped_total", total))
	}
}

func appendReport(b chdriver.Batch, ev ReportEvent) error {
	markers := ev.Markers
	if markers == nil {
		markers = []string{}
	}
	return b.Append(
		utc(ev.EventTime), utc(ev.ReceivedAt), utc(ev.StartedAt),
		ev.TenantID, ev.NamespaceID, ev.SiteID, ev.Site, ev.EndpointGroup, ev.Client,
		ev.IdentityID, ev.IdentityType, ev.ProxyID, ev.LeaseID, ev.ReportID, ev.Node, ev.TokenID,
		ev.URI, ev.Method, ev.HTTPStatus, ev.BusinessCode, ev.ErrorKind, markers,
		ev.Outcome, ev.OutcomeHint, ev.Blame, ev.Rule, ev.LatencyMs, ev.ResponseBytes,
		boolUint8(ev.Suppressed), boolUint8(ev.Late), boolUint8(ev.Probe),
	)
}

func appendLease(b chdriver.Batch, ev LeaseEvent) error {
	return b.Append(
		utc(ev.EventTime),
		ev.TenantID, ev.NamespaceID, ev.SiteID, ev.Site, ev.EndpointGroup, ev.Client,
		ev.IdentityID, ev.ProxyID, ev.LeaseID, ev.Node, ev.TokenID,
		ev.Event, ev.Result, ev.DurationUs, boolUint8(ev.Probe), boolUint8(ev.Sticky),
	)
}

// utc converts t to UTC and clamps it to [Unix epoch, maxEventTime]; the zero
// time maps to the epoch. Clamping keeps out-of-range client timestamps from
// wrapping around in the driver's nanosecond encoding.
func utc(t time.Time) time.Time {
	switch {
	case t.IsZero() || t.Before(minEventTime):
		return minEventTime
	case t.After(maxEventTime):
		return maxEventTime
	default:
		return t.UTC()
	}
}

func boolUint8(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}
