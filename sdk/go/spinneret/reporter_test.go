package spinneret

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
)

// recordingSend is a SendFunc double that records batches and can fail.
type recordingSend struct {
	mu      sync.Mutex
	batches [][]*Report
	fail    func(n int) error // n is the 1-based call number
	calls   int
	resp    func([]*Report) *ReportResponse
	block   chan struct{}
}

func (s *recordingSend) send(ctx context.Context, reports []*Report) (*ReportResponse, error) {
	s.mu.Lock()
	s.calls++
	n := s.calls
	fail, respond, block := s.fail, s.resp, s.block
	s.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, &Error{Code: connect.CodeCanceled, Message: ctx.Err().Error()}
		}
	}
	if fail != nil {
		if err := fail(n); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	s.batches = append(s.batches, append([]*Report(nil), reports...))
	s.mu.Unlock()
	if respond != nil {
		return respond(reports), nil
	}
	return &ReportResponse{Accepted: int32(len(reports))}, nil
}

func (s *recordingSend) batchSizes() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	sizes := make([]int, len(s.batches))
	for i, b := range s.batches {
		sizes[i] = len(b)
	}
	return sizes
}

func (s *recordingSend) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func newTestReporter(t *testing.T, s *recordingSend, opts ReporterOptions) *Reporter {
	t.Helper()
	if opts.InitialBackoff == 0 {
		opts.InitialBackoff = time.Millisecond
	}
	if opts.MaxBackoff == 0 {
		opts.MaxBackoff = 5 * time.Millisecond
	}
	r, err := NewReporter(s.send, opts, discardLogger())
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = r.Close(ctx)
	})
	return r
}

func testReport(i int) *Report {
	return &Report{LeaseId: "lse_1", Uri: "/r", HttpStatus: int32(200 + i%3)}
}

func TestNewReporterValidation(t *testing.T) {
	_, err := NewReporter(nil, ReporterOptions{}, nil)
	require.ErrorContains(t, err, "send function")
	_, err = NewReporter((&recordingSend{}).send, ReporterOptions{BatchSize: -1}, nil)
	require.Error(t, err)
	r, err := NewReporter((&recordingSend{}).send, ReporterOptions{}, nil)
	require.NoError(t, err)
	require.Equal(t, DefaultReportBatchSize, r.Options().BatchSize)
	require.NoError(t, r.Close(context.Background()), "closing an unused reporter")
	require.True(t, r.Closed())
}

func TestReporterBatchesBySize(t *testing.T) {
	s := &recordingSend{}
	r := newTestReporter(t, s, ReporterOptions{FlushInterval: time.Hour, BatchSize: 3, MaxBatchSize: 5, MaxQueueSize: 100})
	for i := range 3 {
		require.NoError(t, r.Submit(testReport(i)))
	}
	eventually(t, 2*time.Second, func() bool { return len(s.batchSizes()) == 1 }, "size-triggered batch")
	require.Equal(t, []int{3}, s.batchSizes())
	stats := r.Stats()
	require.Equal(t, int64(3), stats.Submitted)
	require.Equal(t, int64(3), stats.Sent)
	require.Equal(t, int64(3), stats.Accepted)
	require.Zero(t, stats.Queued)
}

func TestReporterBatchesByInterval(t *testing.T) {
	s := &recordingSend{}
	r := newTestReporter(t, s, ReporterOptions{FlushInterval: 20 * time.Millisecond, BatchSize: 100})
	start := time.Now()
	require.NoError(t, r.Submit(testReport(0)))
	require.NoError(t, r.Submit(testReport(1)))
	eventually(t, 2*time.Second, func() bool { return len(s.batchSizes()) == 1 }, "interval-triggered batch")
	require.GreaterOrEqual(t, time.Since(start), 20*time.Millisecond)
	require.Equal(t, []int{2}, s.batchSizes())

	// The reporter goes back to sleep and wakes up for new reports.
	require.NoError(t, r.Submit(testReport(2)))
	eventually(t, 2*time.Second, func() bool { return len(s.batchSizes()) == 2 }, "second batch")
}

func TestReporterFlushSplitsBatches(t *testing.T) {
	s := &recordingSend{}
	r := newTestReporter(t, s, ReporterOptions{FlushInterval: time.Hour, BatchSize: 2, MaxBatchSize: 2, MaxQueueSize: 10})
	s.mu.Lock()
	s.block = make(chan struct{})
	s.mu.Unlock()
	for i := range 5 {
		require.NoError(t, r.Submit(testReport(i)))
	}
	s.mu.Lock()
	close(s.block)
	s.block = nil
	s.mu.Unlock()
	require.NoError(t, r.Flush(context.Background()))
	sizes := s.batchSizes()
	total := 0
	for _, n := range sizes {
		require.LessOrEqual(t, n, 2)
		total += n
	}
	require.Equal(t, 5, total)
	require.NoError(t, r.Flush(context.Background()), "flushing an empty queue")
}

func TestReporterQueueOverflowDropsOldest(t *testing.T) {
	s := &recordingSend{block: make(chan struct{})}
	r := newTestReporter(t, s, ReporterOptions{FlushInterval: time.Hour, BatchSize: 1, MaxBatchSize: 1, MaxQueueSize: 3})
	require.NoError(t, r.Submit(&Report{ReportId: "a", LeaseId: "l", Uri: "/"}))
	eventually(t, 2*time.Second, func() bool { return s.callCount() == 1 }, "first report in flight")
	for i := 1; i < 10; i++ {
		require.NoError(t, r.Submit(&Report{ReportId: string(rune('a' + i)), LeaseId: "l", Uri: "/"}))
	}
	stats := r.Stats()
	require.Equal(t, int64(10), stats.Submitted)
	// "a" is in flight; the queue keeps the newest three of the other nine.
	require.Equal(t, 3, stats.Queued)
	require.Equal(t, int64(6), stats.Dropped)
	s.mu.Lock()
	close(s.block)
	s.block = nil
	s.mu.Unlock()
	require.NoError(t, r.Flush(context.Background()))
	s.mu.Lock()
	last := s.batches[len(s.batches)-1][0].GetReportId()
	s.mu.Unlock()
	require.Equal(t, "j", last)
	require.Equal(t, int64(10), r.Stats().Sent+r.Stats().Dropped)
}

func TestReporterRetriesRetryableFailures(t *testing.T) {
	s := &recordingSend{fail: func(n int) error {
		switch n {
		case 1:
			return &Error{Code: connect.CodeUnavailable, ErrorKind: ErrorKindConnRefused, Reason: ReasonTransport}
		case 2:
			return &Error{Code: connect.CodeInternal, RetryAfter: 2 * time.Millisecond}
		default:
			return nil
		}
	}}
	r := newTestReporter(t, s, ReporterOptions{FlushInterval: time.Millisecond, BatchSize: 10})
	for i := range 4 {
		require.NoError(t, r.Submit(testReport(i)))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, r.Flush(ctx))
	stats := r.Stats()
	require.Equal(t, int64(2), stats.FailedSends)
	require.Equal(t, int64(4), stats.Sent)
	require.Zero(t, stats.Dropped)
}

func TestReporterDropsOnPermanentFailure(t *testing.T) {
	s := &recordingSend{fail: func(n int) error {
		if n == 1 {
			return &Error{Code: connect.CodePermissionDenied, Reason: ReasonScopeMissing}
		}
		return nil
	}}
	r := newTestReporter(t, s, ReporterOptions{FlushInterval: time.Millisecond})
	require.NoError(t, r.Submit(testReport(0)))
	require.NoError(t, r.Submit(testReport(1)))
	eventually(t, 2*time.Second, func() bool { return r.Stats().FailedSends == 1 && r.Stats().Queued == 0 }, "drop")
	require.NoError(t, r.Flush(context.Background()))
	stats := r.Stats()
	require.Equal(t, int64(2), stats.Dropped+stats.Sent)
	require.Positive(t, stats.Dropped)
}

func TestReporterRejectedCallback(t *testing.T) {
	var got []string
	var mu sync.Mutex
	s := &recordingSend{resp: func(reports []*Report) *ReportResponse {
		rejected := make([]*RejectedReport, 0, len(reports))
		for _, rep := range reports {
			rejected = append(rejected, &RejectedReport{ReportId: rep.GetReportId(), Reason: ReasonLeaseUnknown, Message: "gone"})
		}
		return &ReportResponse{Rejected: rejected, Duplicated: 1}
	}}
	calls := 0
	r := newTestReporter(t, s, ReporterOptions{
		FlushInterval: time.Millisecond,
		OnRejected: func(rej *RejectedReport) {
			mu.Lock()
			defer mu.Unlock()
			calls++
			got = append(got, rej.GetReportId())
			if calls == 1 {
				panic("callback bug")
			}
		},
	})
	for i := range 20 {
		require.NoError(t, r.Submit(&Report{ReportId: "r" + string(rune('a'+i)), LeaseId: "l", Uri: "/"}))
	}
	require.NoError(t, r.Flush(context.Background()))
	eventually(t, 2*time.Second, func() bool { mu.Lock(); defer mu.Unlock(); return len(got) == 20 }, "callbacks")
	stats := r.Stats()
	require.Equal(t, int64(20), stats.Rejected)
	require.Positive(t, stats.Duplicated)
}

func TestReporterSubmitFillsReportID(t *testing.T) {
	s := &recordingSend{}
	r := newTestReporter(t, s, ReporterOptions{FlushInterval: time.Millisecond})
	report := &Report{LeaseId: "l", Uri: "/", LatencyMs: 250}
	require.NoError(t, r.Submit(report))
	require.True(t, validReportID(report.GetReportId()))
	require.NotNil(t, report.GetFinishedAt())
	require.Equal(t, 250*time.Millisecond, report.GetFinishedAt().AsTime().Sub(report.GetStartedAt().AsTime()))
	kept := &Report{ReportId: "keep", LeaseId: "l", Uri: "/", StartedAt: report.GetStartedAt(), FinishedAt: report.GetFinishedAt()}
	require.NoError(t, r.Submit(kept))
	require.Equal(t, "keep", kept.GetReportId())
	require.Same(t, report.GetStartedAt(), kept.GetStartedAt())
	require.Equal(t, ReasonInvalidArgument, ReasonOf(r.Submit(nil)))
}

func TestReporterFlushTimeout(t *testing.T) {
	s := &recordingSend{fail: func(int) error { return &Error{Code: connect.CodeUnavailable} }}
	r := newTestReporter(t, s, ReporterOptions{FlushInterval: time.Hour, MaxBackoff: 2 * time.Millisecond})
	require.NoError(t, r.Submit(testReport(0)))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := r.Flush(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Greater(t, s.callCount(), 1, "failed deliveries are retried while flushing")
}

func TestReporterCloseDelivers(t *testing.T) {
	s := &recordingSend{}
	r := newTestReporter(t, s, ReporterOptions{FlushInterval: time.Hour, BatchSize: 100})
	for i := range 7 {
		require.NoError(t, r.Submit(testReport(i)))
	}
	require.NoError(t, r.Close(context.Background()))
	require.Equal(t, int64(7), r.Stats().Sent)
	require.NoError(t, r.Close(context.Background()), "idempotent")
	require.Equal(t, ReasonReporterClosed, ReasonOf(r.Submit(testReport(0))))
	require.True(t, r.Closed())
	require.NoError(t, r.Flush(context.Background()))
}

func TestReporterCloseDeadlineDrops(t *testing.T) {
	var sends atomic.Int32
	s := &recordingSend{fail: func(int) error {
		sends.Add(1)
		return &Error{Code: connect.CodeUnavailable, ErrorKind: ErrorKindConnRefused}
	}}
	r := newTestReporter(t, s, ReporterOptions{FlushInterval: time.Millisecond, InitialBackoff: time.Hour, MaxBackoff: time.Hour})
	for i := range 5 {
		require.NoError(t, r.Submit(testReport(i)))
	}
	eventually(t, 2*time.Second, func() bool { return sends.Load() >= 1 }, "first delivery attempt")

	// The worker waits for a one-hour backoff: Close interrupts it, retries
	// once and gives up at its deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := r.Close(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorContains(t, err, "dropped 5 reports")
	require.Less(t, time.Since(start), time.Second)
	require.GreaterOrEqual(t, sends.Load(), int32(2))
	stats := r.Stats()
	require.Equal(t, int64(5), stats.Dropped)
	require.Zero(t, stats.Queued)
}

func TestReporterCloseAbortsInflightSend(t *testing.T) {
	s := &recordingSend{block: make(chan struct{})}
	r := newTestReporter(t, s, ReporterOptions{FlushInterval: time.Millisecond, CloseTimeout: 30 * time.Millisecond})
	require.NoError(t, r.Submit(testReport(0)))
	require.NoError(t, r.Submit(testReport(1)))
	eventually(t, 2*time.Second, func() bool { return s.callCount() == 1 }, "send in flight")
	require.NoError(t, r.Submit(testReport(2)))

	start := time.Now()
	err := r.Close(context.Background()) // no deadline: CloseTimeout applies
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), time.Second)
	stats := r.Stats()
	require.Equal(t, int64(3), stats.Dropped)
	require.Equal(t, int64(1), stats.FailedSends)
}

func TestReporterConcurrentSubmit(t *testing.T) {
	s := &recordingSend{}
	r := newTestReporter(t, s, ReporterOptions{FlushInterval: 2 * time.Millisecond, BatchSize: 50, MaxBatchSize: 70})
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Go(func() {
			for i := range 250 {
				if err := r.Submit(testReport(g*1000 + i)); err != nil {
					t.Error(err)
				}
				if i%100 == 0 {
					_ = r.Stats()
				}
			}
		})
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 5 {
			if err := r.Flush(context.Background()); err != nil {
				t.Error(err)
			}
		}
	}()
	wg.Wait()
	require.NoError(t, r.Close(context.Background()))
	stats := r.Stats()
	require.Equal(t, int64(2000), stats.Sent)
	for _, n := range s.batchSizes() {
		require.LessOrEqual(t, n, 70)
	}
}
