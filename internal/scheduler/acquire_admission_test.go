package scheduler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/redis/rueidis"
	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/observability"
	"github.com/TikHub/Spinneret/internal/pkg/admit"
	"github.com/TikHub/Spinneret/internal/store/redis"
)

// pinnedFixture returns a fixture whose acquire limit is pinned to limit, so a
// test can saturate the gate with a single permit instead of 64.
func pinnedFixture(t *testing.T, limit int) *fixture {
	t.Helper()
	f := newFixtureWith(t, func(cfg *Config) {
		cfg.AcquireFleetInflight = DefaultAcquireFleetInflight
		cfg.AcquireMaxInflight = limit
	})
	require.NotNil(t, f.svc.admit, "pinned fixtures always have a gate")
	require.Equal(t, limit, f.svc.admit.Limit())
	return f
}

// holdPermit takes weight units from the gate for the rest of the test.
func holdPermit(t *testing.T, f *fixture, weight int) {
	t.Helper()
	permit, _, err := f.svc.admit.Acquire(context.Background(), weight, 0)
	require.NoError(t, err)
	t.Cleanup(permit.Release)
}

// stubClient wraps the fixture's Redis client so a test can count the commands
// the scheduler sends and make them fail, without touching the shared server or
// closing a client the fixture still has to clean up with.
type stubClient struct {
	rueidis.Client
	// fail, when set, replaces every reply with an error. It is assigned before
	// the client is used.
	fail  error
	calls atomic.Int64
	// onDo, when set, runs after each command completes. It is assigned before
	// the client is used and runs on the caller's goroutine.
	onDo func(n int64)
}

func (c *stubClient) Do(ctx context.Context, cmd rueidis.Completed) rueidis.RedisResult {
	n := c.calls.Add(1)
	res := rueidis.NewErrorResult(c.fail)
	if c.fail == nil {
		res = c.Client.Do(ctx, cmd)
	}
	if c.onDo != nil {
		c.onDo(n)
	}
	return res
}

func (c *stubClient) DoMulti(ctx context.Context, cmds ...rueidis.Completed) []rueidis.RedisResult {
	c.calls.Add(int64(len(cmds)))
	return c.Client.DoMulti(ctx, cmds...)
}

// interceptRedis routes the scheduler's Redis traffic through a stub client and
// returns it. The fixture keeps using the real client for its own assertions.
func interceptRedis(f *fixture) *stubClient {
	c := &stubClient{Client: f.svc.rdb}
	f.svc.rdb = c
	return c
}

// countTimers is an admit.Config.NewTimer that counts the timers a gate asks
// for, which is how "this caller was never parked" is asserted.
type countTimers struct {
	n atomic.Int64
}

func (c *countTimers) new(d time.Duration) admit.Timer {
	c.n.Add(1)
	return realTimerFor(d)
}

// realTimerFor is the production timer behaviour; the counter only observes.
type testTimer struct{ t *time.Timer }

func (t testTimer) C() <-chan time.Time { return t.t.C }

func (t testTimer) Stop() bool { return t.t.Stop() }

func realTimerFor(d time.Duration) admit.Timer { return testTimer{t: time.NewTimer(d)} }

// admissionTotal returns spinneret_acquire_admission_total for one result.
func admissionTotal(t *testing.T, f *fixture, result string) float64 {
	t.Helper()
	var m dto.Metric
	require.NoError(t, f.metrics.AcquireAdmissionTotal.WithLabelValues(result).Write(&m))
	return m.GetCounter().GetValue()
}

// acquireTotal returns spinneret_acquire_total of the fixture's default group.
func acquireTotal(t *testing.T, f *fixture, result string) float64 {
	t.Helper()
	var m dto.Metric
	require.NoError(t, f.metrics.AcquireTotal.WithLabelValues("demo", "_default", result).Write(&m))
	return m.GetCounter().GetValue()
}

// histogram returns the sample count and sum of a plain histogram.
func histogram(t *testing.T, h prometheus.Histogram) (uint64, float64) {
	t.Helper()
	var m dto.Metric
	require.NoError(t, h.Write(&m))
	return m.GetHistogram().GetSampleCount(), m.GetHistogram().GetSampleSum()
}

// gatheredNames returns the metric family names a registry currently exposes.
func gatheredNames(t *testing.T, m *observability.Metrics) map[string]bool {
	t.Helper()
	families, err := m.Registry.Gather()
	require.NoError(t, err)
	out := make(map[string]bool, len(families))
	for _, f := range families {
		out[f.GetName()] = true
	}
	return out
}

func TestAcquireShedsWhenGateIsFull(t *testing.T) {
	f := pinnedFixture(t, 1)
	f.identity(1, 0, []int64{testWebGroup})
	f.setRandoms(0.5)
	holdPermit(t, f, 1)
	stub := interceptRedis(f)

	_, err := f.acquire(f.tokenCtx(), AcquireRequest{Wait: 0})

	e := requireAppErr(t, err, apperr.ReasonOverloaded)
	require.Equal(t, connect.CodeUnavailable, e.Code)
	require.Equal(t, int64(150), e.RetryAfterMs)
	require.ErrorIs(t, err, admit.ErrNoBudget)
	require.ErrorIs(t, err, admit.ErrQueueFull, "ErrNoBudget wraps ErrQueueFull")
	// wait_ms = 0 is the default of every client, so this shed is reported as
	// "the caller declined to wait" and not as a wait room that overflowed.
	require.Equal(t, 1.0, admissionTotal(t, f, admissionShedNoWait))
	require.Zero(t, admissionTotal(t, f, admissionShedQueueFull))
	require.Equal(t, 1.0, acquireTotal(t, f, ResultOverloaded))
	// The load-bearing assertion: a shed issues no Redis command at all, which
	// is why it cannot deepen the queue a release waits behind.
	require.Zero(t, stub.calls.Load(), "a shed acquire must not reach Redis")
	require.Zero(t, f.svc.admit.Queued())
	require.Equal(t, 1, f.svc.admit.InFlight(), "only the permit the test holds")
}

func TestAcquireShedRecordsOverloadedResult(t *testing.T) {
	f := pinnedFixture(t, 1)
	f.identity(1, 0, []int64{testWebGroup})
	holdPermit(t, f, 1)

	_, err := f.acquire(f.tokenCtx(), AcquireRequest{Wait: 0})
	requireReason(t, err, apperr.ReasonOverloaded)
	require.Equal(t, []string{ResultOverloaded}, f.rec.acquireResults())
}

func TestShedOnFirstAttemptReportsOverloaded(t *testing.T) {
	// No identity is seeded, so without the gate this request would answer
	// no_identity_available; the shed must win because nothing reached Redis.
	f := pinnedFixture(t, 1)
	holdPermit(t, f, 1)
	stub := interceptRedis(f)

	_, err := f.acquire(f.tokenCtx(), AcquireRequest{Wait: time.Millisecond})
	requireReason(t, err, apperr.ReasonOverloaded)
	require.Zero(t, stub.calls.Load())
	require.Equal(t, []string{ResultOverloaded}, f.rec.acquireResults())
}

func TestShedAfterRedisReplyKeepsExhausted(t *testing.T) {
	// The gate starts with room for the test's permit plus one attempt. The
	// first attempt reaches Redis and is answered EXHAUSTED; the limit is then
	// lowered from inside that call, so every later attempt is shed. The caller
	// must still be told about the empty pool, because that is its real problem
	// and both SDKs and the console key off that reason.
	f := pinnedFixture(t, 2)
	holdPermit(t, f, 1)
	stub := interceptRedis(f)
	stub.onDo = func(n int64) {
		if n == 1 {
			f.svc.admit.SetLimit(1)
		}
	}

	_, err := f.acquire(f.tokenCtx(), AcquireRequest{Wait: 200 * time.Millisecond})

	e := requireAppErr(t, err, apperr.ReasonNoIdentityAvailable)
	require.Equal(t, connect.CodeResourceExhausted, e.Code)
	require.Equal(t, int64(60000), e.RetryAfterMs, "the script's own retry hint survives")
	require.Equal(t, []string{ResultExhausted}, f.rec.acquireResults())
	require.Equal(t, int64(1), stub.calls.Load(), "only the first attempt reached Redis")
	require.Positive(t, admissionTotal(t, f, admissionShedTimeout), "later attempts waited and were shed")
	require.Zero(t, acquireTotal(t, f, ResultOverloaded))
}

func TestZeroWaitIsNeverParked(t *testing.T) {
	f := pinnedFixture(t, 1)
	timers := &countTimers{}
	// Rebuilt rather than mutated: Config.NewTimer is the gate's only source of
	// deadlines, so counting it proves no caller was parked.
	f.svc.admit = admit.New(admit.Config{Limit: 1, MaxWait: acquireAdmissionWait, NewTimer: timers.new})
	holdPermit(t, f, 1)

	_, err := f.acquire(f.tokenCtx(), AcquireRequest{Wait: 0})
	requireReason(t, err, apperr.ReasonOverloaded)
	require.Zero(t, timers.n.Load(), "wait_ms = 0 must never allocate a timer")

	count, sum := histogram(t, f.metrics.AcquireAdmissionWait)
	require.Equal(t, uint64(1), count)
	require.Zero(t, sum, "a shed without waiting records a zero-valued sample")
}

func TestLadderCreditsAdmissionWait(t *testing.T) {
	t.Parallel()
	// The first rung is fully spent by a 50 ms admission wait, so the retry is
	// immediate instead of sleeping another 50 ms.
	require.Zero(t, ladderDelay(0, acquireAdmissionWait, 400*time.Millisecond))
	require.Equal(t, 50*time.Millisecond, ladderDelay(0, 0, 400*time.Millisecond))
	require.Equal(t, 50*time.Millisecond, ladderDelay(1, acquireAdmissionWait, 400*time.Millisecond))
	// The remaining budget still caps the sleep, and the last rung repeats.
	require.Equal(t, 10*time.Millisecond, ladderDelay(1, 0, 10*time.Millisecond))
	require.Equal(t, waitDelays[len(waitDelays)-1], ladderDelay(99, 0, time.Hour))

	// The ladder spacing is what the credit preserves: with the wait credited
	// against the rung, attempt k begins at exactly the elapsed time it would
	// have begun at without a gate, instead of 50 ms later per attempt.
	require.Equal(t, attemptStarts(8, 0), attemptStarts(8, acquireAdmissionWait))

	// The worked example of the specification: a 400 ms budget buys five
	// attempts whether or not every attempt waits a full rung for its permit.
	require.Equal(t, 5, attemptsBought(400*time.Millisecond, 0))
	require.Equal(t, 5, attemptsBought(400*time.Millisecond, acquireAdmissionWait))
}

// attemptStarts replays n rungs of the retry ladder on a virtual clock with an
// unbounded budget and returns the elapsed time at which each attempt begins
// when every attempt spends waited in the admission wait room.
func attemptStarts(n int, waited time.Duration) []time.Duration {
	starts := make([]time.Duration, 0, n)
	var elapsed time.Duration
	for attempt := range n {
		starts = append(starts, elapsed)
		elapsed += waited
		if delay := ladderDelay(attempt, waited, time.Hour); delay > 0 {
			elapsed += delay
		}
	}
	return starts
}

// attemptsBought replays the retry ladder of acquireWithWait on a virtual clock
// and returns how many attempts a wait budget buys when every attempt spends
// waited in the admission wait room.
func attemptsBought(wait, waited time.Duration) int {
	var elapsed time.Duration
	for attempt := 0; ; attempt++ {
		elapsed += waited
		remaining := wait - elapsed
		if remaining <= 0 {
			return attempt + 1
		}
		if delay := ladderDelay(attempt, waited, remaining); delay > 0 {
			elapsed += delay
		}
	}
}

func TestAcquireSucceedsWhenGateHasRoom(t *testing.T) {
	f := pinnedFixture(t, 4)
	f.identity(1, 0, []int64{testWebGroup})
	scripts, _ := histogram(t, f.metrics.AcquireScriptDuration)

	g := f.mustAcquire(AcquireRequest{})
	require.Equal(t, "idt_1", g.Lease.IdentityID)

	require.Equal(t, 1.0, admissionTotal(t, f, admissionImmediate))
	require.Zero(t, admissionTotal(t, f, admissionShedQueueFull))
	require.Zero(t, admissionTotal(t, f, admissionQueued))
	after, _ := histogram(t, f.metrics.AcquireScriptDuration)
	require.Equal(t, scripts+1, after)
	require.Zero(t, f.svc.admit.InFlight(), "the permit is released with the call")
}

func TestAcquireBatchTakesCountPermits(t *testing.T) {
	// A batch is charged count permits, not one: with five of ten units held a
	// six-lease batch does not fit and is shed, while a five-lease batch does.
	f := pinnedFixture(t, 10)
	for i := int64(1); i <= 5; i++ {
		f.identity(i, 0, []int64{testWebGroup})
	}
	holdPermit(t, f, 5)

	_, err := f.svc.AcquireBatch(f.tokenCtx(), AcquireRequest{Site: "demo", Client: "web", Count: 6})
	requireReason(t, err, apperr.ReasonOverloaded)

	grants, err := f.svc.AcquireBatch(f.tokenCtx(), AcquireRequest{Site: "demo", Client: "web", Count: 5})
	require.NoError(t, err)
	require.Len(t, grants, 5)
	require.Equal(t, 5, f.svc.admit.InFlight(), "only the permit the test holds")
}

func TestGateReleasedOnRedisError(t *testing.T) {
	f := pinnedFixture(t, 2)
	stub := interceptRedis(f)
	stub.fail = errors.New("redis unavailable")

	_, err := f.acquire(f.tokenCtx(), AcquireRequest{})
	requireReason(t, err, apperr.ReasonInternal)
	require.Zero(t, f.svc.admit.InFlight(), "the permit must be released on the error path")
	require.Zero(t, f.svc.admit.Queued())
}

func TestGateReleasedOnContextCancel(t *testing.T) {
	f := pinnedFixture(t, 2)
	f.identity(1, 0, []int64{testWebGroup})
	ctx, cancel := context.WithCancel(f.tokenCtx())
	cancel()

	_, err := f.acquire(ctx, AcquireRequest{})
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, f.svc.admit.InFlight())
	require.Zero(t, f.svc.admit.Queued())
}

func TestSetAcquirePeersDividesBudget(t *testing.T) {
	t.Parallel()
	newService := func(cfg Config) *Service {
		return New(cfg, nil, redis.Keys{}, nil, nil, nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}

	tests := []struct {
		name  string
		peers int
		want  int
	}{
		{name: "alone", peers: 1, want: 64},
		{name: "two", peers: 2, want: 32},
		{name: "four", peers: 4, want: 16},
		{name: "eight", peers: 8, want: 8},
		{name: "sixteen", peers: 16, want: 4},
		{name: "past the floor", peers: 100, want: 4},
		{name: "nonsense count ignored", peers: 0, want: 64},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newService(Config{ReportShards: testShards, AcquireFleetInflight: DefaultAcquireFleetInflight})
			s.SetAcquirePeers(tc.peers)
			require.Equal(t, tc.want, s.admit.Limit())
			if tc.peers >= 1 {
				require.Equal(t, tc.peers, s.acquirePeers())
			} else {
				require.Equal(t, 1, s.acquirePeers(), "the divisor is never lowered below one")
			}
		})

		t.Run(tc.name+" pinned", func(t *testing.T) {
			t.Parallel()
			s := newService(Config{
				ReportShards:         testShards,
				AcquireFleetInflight: DefaultAcquireFleetInflight,
				AcquireMaxInflight:   32,
			})
			s.SetAcquirePeers(tc.peers)
			require.Equal(t, 32, s.admit.Limit(), "a pinned limit is never divided")
			require.Equal(t, 1, s.acquirePeers())
		})
	}

	t.Run("disabled", func(t *testing.T) {
		t.Parallel()
		s := newService(Config{ReportShards: testShards})
		require.Nil(t, s.admit)
		s.SetAcquirePeers(4) // must not panic
		require.Zero(t, s.admit.Limit())
	})

	t.Run("clamped ceiling", func(t *testing.T) {
		t.Parallel()
		s := newService(Config{ReportShards: testShards, AcquireFleetInflight: 65536})
		require.Equal(t, maxAcquireInflight, s.admit.Limit())
	})
}

func TestAdmissionDisabled(t *testing.T) {
	f := newFixtureWith(t, func(cfg *Config) {
		cfg.AcquireFleetInflight = 0
		cfg.AcquireMaxInflight = 0
	})
	require.Nil(t, f.svc.admit, "no gate is constructed when admission control is off")

	// An exhausted pool answers exactly as it does without admission control.
	_, err := f.acquire(f.tokenCtx(), AcquireRequest{})
	e := requireAppErr(t, err, apperr.ReasonNoIdentityAvailable)
	require.Equal(t, connect.CodeResourceExhausted, e.Code)
	require.Equal(t, int64(60000), e.RetryAfterMs)
	require.Equal(t, []string{ResultExhausted}, f.rec.acquireResults())

	// And a healthy acquire still succeeds without touching the gate.
	f.identity(1, 0, []int64{testWebGroup})
	g := f.mustAcquire(AcquireRequest{})
	require.Equal(t, "idt_1", g.Lease.IdentityID)

	// No admission decision is recorded and no gate gauge is registered, so the
	// exported metric surface is the pre-gate one.
	names := gatheredNames(t, f.metrics)
	for _, name := range []string{
		"spinneret_acquire_admission_total",
		"spinneret_acquire_inflight",
		"spinneret_acquire_inflight_limit",
		"spinneret_acquire_queued",
		"spinneret_acquire_peers",
	} {
		require.NotContains(t, names, name, "%s must not exist with admission control off", name)
	}
	// The wait histogram is registered unconditionally, so it is its emptiness
	// that has to be asserted.
	count, _ := histogram(t, f.metrics.AcquireAdmissionWait)
	require.Zero(t, count)
}

func TestResultOfOverloaded(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "overloaded", err: apperr.Unavailable(apperr.ReasonOverloaded, 150, "shed"), want: ResultOverloaded},
		{name: "exhausted", err: apperr.ResourceExhausted(apperr.ReasonNoIdentityAvailable, 50, "empty"), want: ResultExhausted},
		{name: "no proxy", err: apperr.ResourceExhausted(apperr.ReasonNoProxyAvailable, 50, "none"), want: ResultNoProxy},
		{name: "circuit", err: apperr.Unavailable(apperr.ReasonCircuitOpen, 50, "open"), want: ResultCircuitOpen},
		{name: "other", err: errors.New("boom"), want: ResultError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, resultOf(acquireOutcome{}, tc.err))
		})
	}
}

func TestFailedAttemptAfterRedisReplyRecordsError(t *testing.T) {
	// A failed attempt must report no outcome, which is what it did before the
	// retry loop kept one across attempts: the first attempt is answered
	// EXHAUSTED, the second fails with the caller's context already cancelled,
	// and the recorded statistics result must stay error. Attributing it to
	// exhausted instead would move the console's acquire failure ratio for
	// requests that were merely cancelled — with admission control switched off
	// as much as on.
	f := pinnedFixture(t, 4)
	ctx, cancel := context.WithCancel(f.tokenCtx())
	stub := interceptRedis(f)
	stub.onDo = func(n int64) {
		switch n {
		case 1:
			// The first attempt reached Redis and was answered; every later one
			// fails, with the caller gone by the time the error is returned.
			stub.fail = errors.New("redis unavailable")
		case 2:
			cancel()
		}
	}

	_, err := f.acquire(ctx, AcquireRequest{Wait: 200 * time.Millisecond})

	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, int64(2), stub.calls.Load(), "the second attempt is the one that failed")
	require.Equal(t, []string{ResultError}, f.rec.acquireResults())
	require.Zero(t, f.svc.admit.InFlight())
}

func TestLadderRetriesThroughTheGate(t *testing.T) {
	// The integration half of the ladder credit: with the gate saturated for the
	// whole call, every attempt spends its rung waiting for a permit and the
	// credit lets the next attempt start immediately, so a wait budget still buys
	// several attempts instead of one. The assertion is a count with wide margin,
	// never a duration: two attempts need 150 ms of the 400 ms budget.
	f := pinnedFixture(t, 1)
	f.identity(1, 0, []int64{testWebGroup})
	holdPermit(t, f, 1)
	stub := interceptRedis(f)

	_, err := f.acquire(f.tokenCtx(), AcquireRequest{Wait: 400 * time.Millisecond})

	requireReason(t, err, apperr.ReasonOverloaded)
	require.Zero(t, stub.calls.Load(), "no attempt reached Redis")
	require.GreaterOrEqual(t, admissionTotal(t, f, admissionShedTimeout), 2.0,
		"the retry ladder ran more than once through the gate")
	require.Equal(t, []string{ResultOverloaded}, f.rec.acquireResults())
}

func TestUnpinnedFixtureRunsAtTheProductionLimit(t *testing.T) {
	// The rest of the suite pins a wide limit so that no existing test sheds; this
	// one case runs at the unpinned production default, so the claim that the
	// suite exercises the shipped configuration is literally true for the divided
	// path too.
	f := newFixtureWith(t, func(cfg *Config) {
		cfg.AcquireFleetInflight = DefaultAcquireFleetInflight
		cfg.AcquireMaxInflight = 0
	})
	require.NotNil(t, f.svc.admit)
	require.Equal(t, DefaultAcquireFleetInflight, f.svc.admit.Limit(), "one instance takes the whole budget")
	require.Equal(t, 1, f.svc.acquirePeers())

	f.identity(1, 0, []int64{testWebGroup})
	g := f.mustAcquire(AcquireRequest{})
	require.Equal(t, "idt_1", g.Lease.IdentityID)
	require.Equal(t, 1.0, admissionTotal(t, f, admissionImmediate))

	// And a second live instance halves it, which is the path a pinned limit
	// never takes.
	f.svc.SetAcquirePeers(2)
	require.Equal(t, DefaultAcquireFleetInflight/2, f.svc.admit.Limit())
	require.Equal(t, 2, f.svc.acquirePeers())
}
