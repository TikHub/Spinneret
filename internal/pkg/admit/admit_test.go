package admit

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeTimer is a Timer whose deadline the test fires by hand.
type fakeTimer struct {
	ch      chan time.Time
	stopped atomic.Bool
}

func (f *fakeTimer) C() <-chan time.Time { return f.ch }

func (f *fakeTimer) Stop() bool { return f.stopped.CompareAndSwap(false, true) }

// fire delivers the deadline. The channel is buffered, so it never blocks.
func (f *fakeTimer) fire() { f.ch <- time.Unix(0, 0) }

// timerFactory records every requested deadline so tests can assert the
// duration the gate asked for, and that it asked at all.
type timerFactory struct {
	mu        sync.Mutex
	durations []time.Duration
	timers    []*fakeTimer
}

func (tf *timerFactory) newTimer(d time.Duration) Timer {
	t := &fakeTimer{ch: make(chan time.Time, 1)}
	tf.mu.Lock()
	defer tf.mu.Unlock()
	tf.durations = append(tf.durations, d)
	tf.timers = append(tf.timers, t)
	return t
}

func (tf *timerFactory) count() int {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	return len(tf.timers)
}

func (tf *timerFactory) duration(i int) time.Duration {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	return tf.durations[i]
}

func (tf *timerFactory) timer(i int) *fakeTimer {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	return tf.timers[i]
}

// newNeverTimer returns a Timer that never fires, for tests where the only
// interesting exit from the wait room is the handoff.
func newNeverTimer(time.Duration) Timer {
	return &fakeTimer{ch: make(chan time.Time)}
}

// manualClock is the gate's clock under test control.
type manualClock struct {
	mu sync.Mutex
	t  time.Time
}

func newManualClock() *manualClock {
	return &manualClock{t: time.Unix(1700000000, 0).UTC()}
}

func (c *manualClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *manualClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// acquireResult is one Acquire return, carried off a goroutine.
type acquireResult struct {
	permit Permit
	res    Result
	err    error
}

// harness is a Gate with a manual clock, hand-fired timers and the park hook
// wired up, so every ordering in a test is explicit.
type harness struct {
	gate    *Gate
	clock   *manualClock
	timers  *timerFactory
	entered chan struct{}
}

func newHarness(cfg Config) *harness {
	h := &harness{
		clock:   newManualClock(),
		timers:  &timerFactory{},
		entered: make(chan struct{}),
	}
	cfg.Now = h.clock.now
	cfg.NewTimer = h.timers.newTimer
	h.gate = New(cfg)
	h.gate.entered = h.entered
	return h
}

// take admits a caller that must not wait, and fails the test otherwise.
func (h *harness) take(t *testing.T, weight int) Permit {
	t.Helper()
	p, res, err := h.gate.Acquire(context.Background(), weight, 0)
	require.NoError(t, err)
	require.False(t, res.Queued)
	require.Zero(t, res.Waited)
	return p
}

// park starts a caller that is expected to park, and returns once it is in the
// wait room with its timer created.
func (h *harness) park(t *testing.T, ctx context.Context, weight int, budget time.Duration) <-chan acquireResult {
	t.Helper()
	out := make(chan acquireResult, 1)
	go func() {
		p, res, err := h.gate.Acquire(ctx, weight, budget)
		out <- acquireResult{permit: p, res: res, err: err}
	}()
	<-h.entered
	return out
}

// drain waits for parked callers to return and releases whatever they got, so
// no goroutine outlives the test.
func drain(chans ...<-chan acquireResult) {
	for _, ch := range chans {
		r := <-ch
		r.permit.Release()
	}
}

// empty reports whether a result channel is still waiting, without blocking.
func empty(ch <-chan acquireResult) bool {
	select {
	case <-ch:
		return false
	default:
		return true
	}
}

func TestAdmitsUpToLimit(t *testing.T) {
	t.Parallel()
	h := newHarness(Config{Limit: 3})

	first := h.take(t, 1)
	h.take(t, 1)
	h.take(t, 1)
	require.Equal(t, 3, h.gate.InFlight())
	require.Equal(t, 3, h.gate.Limit())
	require.Equal(t, 0, h.gate.Queued())

	// A fourth caller without a budget is shed while the gate is full.
	_, _, err := h.gate.Acquire(context.Background(), 1, 0)
	require.ErrorIs(t, err, ErrQueueFull)

	first.Release()
	require.Equal(t, 2, h.gate.InFlight())
	h.take(t, 1)
	require.Equal(t, 3, h.gate.InFlight())
}

func TestZeroBudgetNeverParks(t *testing.T) {
	t.Parallel()
	h := newHarness(Config{Limit: 1})
	held := h.take(t, 1)
	defer held.Release()

	p, res, err := h.gate.Acquire(context.Background(), 1, 0)
	require.ErrorIs(t, err, ErrQueueFull)
	require.False(t, res.Queued)
	require.Zero(t, res.Waited)
	require.Equal(t, 0, h.gate.Queued())
	require.Equal(t, 1, h.gate.InFlight())
	require.Equal(t, 0, h.timers.count(), "a caller with no budget must not allocate a timer")

	// The zero permit is safe to release and changes no accounting.
	p.Release()
	require.Equal(t, 1, h.gate.InFlight())
}

func TestNegativeBudgetNeverParks(t *testing.T) {
	t.Parallel()
	h := newHarness(Config{Limit: 1})
	held := h.take(t, 1)
	defer held.Release()

	_, _, err := h.gate.Acquire(context.Background(), 1, -5*time.Millisecond)
	require.ErrorIs(t, err, ErrQueueFull)
	require.Equal(t, 0, h.gate.Queued())
	require.Equal(t, 0, h.timers.count())
}

func TestParksWithinBudget(t *testing.T) {
	t.Parallel()
	h := newHarness(Config{Limit: 1, MaxWait: 50 * time.Millisecond})
	held := h.take(t, 1)

	out := h.park(t, context.Background(), 1, time.Second)
	require.Equal(t, 1, h.timers.count())
	require.Equal(t, 50*time.Millisecond, h.timers.duration(0), "a budget above MaxWait is capped by MaxWait")
	require.Equal(t, 1, h.gate.Queued())

	held.Release()
	r := <-out
	require.NoError(t, r.err)
	require.True(t, r.res.Queued)
	require.Equal(t, 1, h.gate.InFlight())
	r.permit.Release()
	require.Equal(t, 0, h.gate.InFlight())
	require.Equal(t, 0, h.gate.Queued())
}

func TestParkUsesBudgetWhenSmaller(t *testing.T) {
	t.Parallel()
	h := newHarness(Config{Limit: 1, MaxWait: 50 * time.Millisecond})
	held := h.take(t, 1)

	out := h.park(t, context.Background(), 1, 10*time.Millisecond)
	require.Equal(t, 10*time.Millisecond, h.timers.duration(0), "a budget below MaxWait wins")

	held.Release()
	drain(out)
}

func TestShedsWhenWaitRoomFull(t *testing.T) {
	t.Parallel()
	h := newHarness(Config{Limit: 1, QueueDepth: 4})
	held := h.take(t, 1)

	ctx, cancel := context.WithCancel(context.Background())
	parked := make([]<-chan acquireResult, 0, 4)
	for range 4 {
		parked = append(parked, h.park(t, ctx, 1, time.Second))
	}
	require.Equal(t, 4, h.gate.Queued())

	// The fifth caller has a budget but no room, so it is shed synchronously.
	_, res, err := h.gate.Acquire(context.Background(), 1, time.Second)
	require.ErrorIs(t, err, ErrQueueFull)
	require.False(t, res.Queued)
	require.Equal(t, 4, h.timers.count(), "a shed caller must not allocate a timer")
	require.Equal(t, 4, h.gate.Queued())

	// One release admits the head; cancelling clears the rest.
	held.Release()
	cancel()
	drain(parked...)
	require.Equal(t, 0, h.gate.Queued())
	require.Equal(t, 0, h.gate.InFlight())
}

func TestShedsOnQueueTimeout(t *testing.T) {
	t.Parallel()
	h := newHarness(Config{Limit: 1, MaxWait: 50 * time.Millisecond})
	held := h.take(t, 1)
	defer held.Release()

	out := h.park(t, context.Background(), 1, time.Second)
	h.clock.advance(30 * time.Millisecond)
	h.timers.timer(0).fire()

	r := <-out
	require.ErrorIs(t, r.err, ErrQueueTimeout)
	require.True(t, r.res.Queued)
	require.Equal(t, 30*time.Millisecond, r.res.Waited)
	require.Equal(t, 0, h.gate.Queued())
	require.Equal(t, 1, h.gate.InFlight(), "a timed-out caller must not change accounting")

	r.permit.Release()
	require.Equal(t, 1, h.gate.InFlight())
}

func TestHonoursContextCancellation(t *testing.T) {
	t.Parallel()
	h := newHarness(Config{Limit: 1})
	held := h.take(t, 1)
	defer held.Release()

	ctx, cancel := context.WithCancel(context.Background())
	out := h.park(t, ctx, 1, time.Second)
	cancel()

	r := <-out
	require.ErrorIs(t, r.err, context.Canceled)
	require.True(t, r.res.Queued)
	require.Equal(t, 0, h.gate.Queued())
	require.Equal(t, 1, h.gate.InFlight())

	r.permit.Release()
	require.Equal(t, 1, h.gate.InFlight(), "a cancelled caller must not leak or return a permit")
}

func TestTimedOutWaiterFreesSlotForNext(t *testing.T) {
	t.Parallel()
	h := newHarness(Config{Limit: 1, QueueDepth: 2})
	held := h.take(t, 1)

	first := h.park(t, context.Background(), 1, time.Second)
	second := h.park(t, context.Background(), 1, time.Second)
	require.Equal(t, 2, h.gate.Queued())

	h.clock.advance(time.Millisecond)
	h.timers.timer(0).fire()
	r := <-first
	require.ErrorIs(t, r.err, ErrQueueTimeout)
	require.Equal(t, 1, h.gate.Queued(), "the timed-out caller must free its wait-room slot")

	// A third caller now fits in the room the timed-out one vacated.
	third := h.park(t, context.Background(), 1, time.Second)
	require.Equal(t, 2, h.gate.Queued())

	held.Release()
	r = <-second
	require.NoError(t, r.err, "the next waiter still gets the permit")
	require.True(t, r.res.Queued)
	r.permit.Release()
	drain(third)
}

func TestHandoffRaceResolvedExactlyOnce(t *testing.T) {
	t.Parallel()
	const iterations = 1000
	admitted, shed := 0, 0

	for range iterations {
		h := newHarness(Config{Limit: 1, MaxWait: 50 * time.Millisecond})
		held := h.take(t, 1)
		out := h.park(t, context.Background(), 1, time.Second)

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			held.Release()
		}()
		go func() {
			defer wg.Done()
			h.clock.advance(time.Millisecond)
			h.timers.timer(0).fire()
		}()
		wg.Wait()

		r := <-out
		if r.err == nil {
			admitted++
			require.Equal(t, 1, h.gate.InFlight())
			r.permit.Release()
		} else {
			shed++
			require.ErrorIs(t, r.err, ErrQueueTimeout)
		}
		require.Equal(t, 0, h.gate.InFlight(), "a permit is never lost nor double-issued")
		require.Equal(t, 0, h.gate.Queued())
	}

	require.Equal(t, iterations, admitted+shed, "every attempt resolves exactly once")
}

func TestDoesNotBargePastWaiters(t *testing.T) {
	t.Parallel()
	h := newHarness(Config{Limit: 1})
	held := h.take(t, 1)

	first := h.park(t, context.Background(), 1, time.Second)
	// A fresh arrival must park behind the waiter instead of taking the slot
	// the release below frees.
	second := h.park(t, context.Background(), 1, time.Second)
	require.Equal(t, 2, h.gate.Queued())

	held.Release()
	r := <-first
	require.NoError(t, r.err)
	require.Equal(t, 1, h.gate.Queued())
	require.True(t, empty(second), "the later arrival must not be admitted first")

	r.permit.Release()
	drain(second)
	require.Equal(t, 0, h.gate.InFlight())
}

func TestFIFO(t *testing.T) {
	t.Parallel()
	const waiters = 8
	h := newHarness(Config{Limit: 1})
	held := h.take(t, 1)

	outs := make([]<-chan acquireResult, 0, waiters)
	for range waiters {
		outs = append(outs, h.park(t, context.Background(), 1, time.Second))
	}
	require.Equal(t, waiters, h.gate.Queued())

	release := func() { held.Release() }
	for i := range waiters {
		release()
		r := <-outs[i]
		require.NoError(t, r.err, "waiter %d", i)
		require.True(t, r.res.Queued)
		for j := i + 1; j < waiters; j++ {
			require.True(t, empty(outs[j]), "waiter %d was admitted before waiter %d", j, i)
		}
		permit := r.permit
		release = permit.Release
	}
	release()
	require.Equal(t, 0, h.gate.InFlight())
	require.Equal(t, 0, h.gate.Queued())
}

func TestWeightedAdmission(t *testing.T) {
	t.Parallel()
	h := newHarness(Config{Limit: 10})

	first := h.take(t, 6)
	require.Equal(t, 6, h.gate.InFlight())

	// A second six-unit caller does not fit alongside the first.
	out := h.park(t, context.Background(), 6, time.Second)
	require.Equal(t, 1, h.gate.Queued())
	require.Equal(t, 6, h.gate.InFlight())

	first.Release()
	r := <-out
	require.NoError(t, r.err)
	require.Equal(t, 6, h.gate.InFlight())
	r.permit.Release()
	require.Equal(t, 0, h.gate.InFlight())
}

func TestWeightClampedToLimit(t *testing.T) {
	t.Parallel()
	h := newHarness(Config{Limit: 4})

	p := h.take(t, 50)
	require.Equal(t, 4, h.gate.InFlight(), "a request larger than the limit is clamped, not rejected")

	p.Release()
	require.Equal(t, 0, h.gate.InFlight())

	// Weights below one are raised to one.
	q := h.take(t, 0)
	require.Equal(t, 1, h.gate.InFlight())
	q.Release()
	r := h.take(t, -3)
	require.Equal(t, 1, h.gate.InFlight())
	r.Release()
	require.Equal(t, 0, h.gate.InFlight())
}

func TestSetLimitWakesWaiters(t *testing.T) {
	t.Parallel()
	h := newHarness(Config{Limit: 1})
	held := h.take(t, 1)

	first := h.park(t, context.Background(), 1, time.Second)
	second := h.park(t, context.Background(), 1, time.Second)
	require.Equal(t, 2, h.gate.Queued())

	// Raising the limit admits both without the held permit being released.
	h.gate.SetLimit(3)
	r1, r2 := <-first, <-second
	require.NoError(t, r1.err)
	require.NoError(t, r2.err)
	require.Equal(t, 3, h.gate.Limit())
	require.Equal(t, 3, h.gate.InFlight())
	require.Equal(t, 0, h.gate.Queued())

	// Lowering it below what is in flight revokes nothing and admits nothing.
	h.gate.SetLimit(1)
	require.Equal(t, 1, h.gate.Limit())
	require.Equal(t, 3, h.gate.InFlight())

	third := h.park(t, context.Background(), 1, time.Second)
	held.Release()
	require.Equal(t, 2, h.gate.InFlight())
	require.True(t, empty(third))
	r1.permit.Release()
	require.Equal(t, 1, h.gate.InFlight())
	require.True(t, empty(third))

	r2.permit.Release()
	r3 := <-third
	require.NoError(t, r3.err, "the waiter is admitted once the shrunken limit has room")
	require.Equal(t, 1, h.gate.InFlight())
	r3.permit.Release()
	require.Equal(t, 0, h.gate.InFlight())
}

func TestSetLimitReclampsParkedWeight(t *testing.T) {
	t.Parallel()
	h := newHarness(Config{Limit: 10})
	held := h.take(t, 10)

	// The caller parks asking for eight units, then the limit drops to four.
	out := h.park(t, context.Background(), 8, time.Second)
	h.gate.SetLimit(4)
	require.Equal(t, 10, h.gate.InFlight(), "in-flight weight above the new limit is not revoked")

	held.Release()
	r := <-out
	require.NoError(t, r.err, "a parked caller wider than the new limit is still served")
	require.Equal(t, 4, h.gate.InFlight())
	r.permit.Release()
	require.Equal(t, 0, h.gate.InFlight())
}

func TestSetLimitRederivesWaitRoomDepth(t *testing.T) {
	t.Parallel()
	t.Run("derived", func(t *testing.T) {
		t.Parallel()
		h := newHarness(Config{Limit: 1})
		// The derived depth starts at the floor and grows with the limit.
		require.Equal(t, MinQueueDepth, h.gate.depth)
		h.gate.SetLimit(9)
		require.Equal(t, QueueFactor*9, h.gate.depth)

		held := h.take(t, 9)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		parked := make([]<-chan acquireResult, 0, QueueFactor*9)
		for range QueueFactor * 9 {
			parked = append(parked, h.park(t, ctx, 1, time.Second))
		}
		require.Equal(t, QueueFactor*9, h.gate.Queued())

		_, _, err := h.gate.Acquire(context.Background(), 1, time.Second)
		require.ErrorIs(t, err, ErrQueueFull, "the re-derived depth is the new cap")

		held.Release()
		cancel()
		drain(parked...)
		require.Equal(t, 0, h.gate.InFlight())
	})

	t.Run("configured", func(t *testing.T) {
		t.Parallel()
		h := newHarness(Config{Limit: 1, QueueDepth: 2})
		h.gate.SetLimit(9)
		require.Equal(t, 2, h.gate.depth, "an explicit QueueDepth is never re-derived")

		held := h.take(t, 9)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		first := h.park(t, ctx, 1, time.Second)
		second := h.park(t, ctx, 1, time.Second)

		_, _, err := h.gate.Acquire(context.Background(), 1, time.Second)
		require.ErrorIs(t, err, ErrQueueFull)

		held.Release()
		cancel()
		drain(first, second)
		require.Equal(t, 0, h.gate.InFlight())
	})
}

func TestNewClampsLimit(t *testing.T) {
	t.Parallel()
	for _, limit := range []int{-1, 0} {
		g := New(Config{Limit: limit})
		require.Equal(t, 1, g.Limit())
	}
	g := New(Config{Limit: 5})
	g.SetLimit(0)
	require.Equal(t, 1, g.Limit())
	g.SetLimit(-7)
	require.Equal(t, 1, g.Limit())
}

func TestDefaultMaxWaitApplies(t *testing.T) {
	t.Parallel()
	h := newHarness(Config{Limit: 1})
	held := h.take(t, 1)

	out := h.park(t, context.Background(), 1, time.Hour)
	require.Equal(t, DefaultMaxWait, h.timers.duration(0))

	held.Release()
	drain(out)
}

func TestReleaseIsIdempotent(t *testing.T) {
	t.Parallel()
	h := newHarness(Config{Limit: 1})

	p := h.take(t, 1)
	p.Release()
	p.Release()
	require.Equal(t, 0, h.gate.InFlight())

	// A zero permit and a nil receiver are both safe.
	var zero Permit
	zero.Release()
	var nilPermit *Permit
	nilPermit.Release()
	require.Equal(t, 0, h.gate.InFlight())
}

func TestNilGateAdmitsEverything(t *testing.T) {
	t.Parallel()
	var g *Gate

	p, res, err := g.Acquire(context.Background(), 5, 0)
	require.NoError(t, err)
	require.Equal(t, Result{}, res)
	require.Equal(t, Permit{}, p)
	p.Release()

	g.SetLimit(10)
	require.Equal(t, 0, g.Limit())
	require.Equal(t, 0, g.InFlight())
	require.Equal(t, 0, g.Queued())
}

func TestConcurrentInvariant(t *testing.T) {
	t.Parallel()
	const (
		limit      = 8
		goroutines = 200
		iterations = 500
	)
	g := New(Config{
		Limit:      limit,
		MaxWait:    time.Second,
		QueueDepth: 4096,
		NewTimer:   newNeverTimer,
	})

	var highWater atomic.Int64
	observe := func() {
		g.mu.Lock()
		n := int64(g.inFlight)
		g.mu.Unlock()
		for {
			cur := highWater.Load()
			if n <= cur || highWater.CompareAndSwap(cur, n) {
				return
			}
		}
	}

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			ctx := context.Background()
			for range iterations {
				weight := rand.IntN(5) + 1
				p, _, err := g.Acquire(ctx, weight, time.Second)
				if err != nil {
					continue
				}
				observe()
				p.Release()
			}
		}()
	}
	wg.Wait()

	require.LessOrEqual(t, highWater.Load(), int64(limit), "in-flight weight never exceeds the limit")
	require.Equal(t, 0, g.InFlight())
	require.Equal(t, 0, g.Queued())
}

// BenchmarkGate reports rather than asserts: a wall-clock threshold in CI is
// precisely the kind of flaky gate the house rules forbid. Run it as
//
//	go test -run XXX -bench BenchmarkGate -benchtime 300000x -cpu 1,8,64 ./internal/pkg/admit/
//
// and compare against the numbers recorded in documents/en/17-performance.md;
// the flat result from 8 to 64 goroutines is the evidence for choosing a mutex
// over a channel semaphore, which a resizable limit rules out anyway.
func BenchmarkGate(b *testing.B) {
	// The limit is far above the parallelism so the benchmark measures the
	// uncontended fast path and the lock under contention, not shedding.
	g := New(Config{Limit: 1 << 20})
	ctx := context.Background()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			p, _, err := g.Acquire(ctx, 1, 0)
			if err != nil && !errors.Is(err, ErrQueueFull) {
				b.Error(err)
				return
			}
			p.Release()
		}
	})
}

func TestNoBudgetShedIsDistinguishableFromFullWaitRoom(t *testing.T) {
	t.Parallel()
	// The two shed causes say different things about how deep the overload is:
	// "the caller declined to wait" is the normal default, while "the wait room
	// overflowed" means even the callers that offered to wait could not be
	// parked. Both are ErrQueueFull so that a caller which only cares about
	// admission can match one value.
	h := newHarness(Config{Limit: 1, QueueDepth: 1})
	held := h.take(t, 1)

	_, _, err := h.gate.Acquire(context.Background(), 1, 0)
	require.ErrorIs(t, err, ErrNoBudget)
	require.ErrorIs(t, err, ErrQueueFull, "ErrNoBudget wraps ErrQueueFull")

	ctx, cancel := context.WithCancel(context.Background())
	parked := h.park(t, ctx, 1, time.Second)
	_, _, err = h.gate.Acquire(context.Background(), 1, time.Second)
	require.ErrorIs(t, err, ErrQueueFull)
	require.NotErrorIs(t, err, ErrNoBudget, "a full wait room is not a missing budget")

	held.Release()
	cancel()
	drain(parked)
	require.Equal(t, 0, h.gate.InFlight())
}

func TestParkedWeightRestoredWhenLimitRecovers(t *testing.T) {
	t.Parallel()
	// A caller parked across a shrink and a recovery must be charged for the
	// work it actually does. Charging it the shrunken weight would let a batch
	// of N leases hold fewer than N units of Redis concurrency, which is exactly
	// what weighting the permit by the batch size exists to prevent.
	h := newHarness(Config{Limit: 10})
	held := h.take(t, 10)

	out := h.park(t, context.Background(), 8, time.Second)
	h.gate.SetLimit(3)
	require.True(t, empty(out), "the waiter does not fit under the lowered limit")

	h.gate.SetLimit(64)
	r := <-out
	require.NoError(t, r.err)
	require.Equal(t, 18, h.gate.InFlight(), "the waiter is admitted with the eight units it asked for")

	held.Release()
	require.Equal(t, 8, h.gate.InFlight())
	r.permit.Release()
	require.Equal(t, 0, h.gate.InFlight())
}

func TestExtremeLimitDoesNotOverflow(t *testing.T) {
	t.Parallel()
	// admit is a general-purpose package and the benchmark already constructs a
	// gate with a limit of 1<<20; a limit near math.MaxInt must still bound
	// admissions instead of wrapping the capacity check and admitting forever.
	g := New(Config{Limit: math.MaxInt})
	require.Equal(t, math.MaxInt, g.Limit())

	p, _, err := g.Acquire(context.Background(), math.MaxInt, 0)
	require.NoError(t, err)
	require.Equal(t, math.MaxInt, g.InFlight())

	_, _, err = g.Acquire(context.Background(), math.MaxInt, 0)
	require.ErrorIs(t, err, ErrQueueFull, "the gate is full, not empty")
	require.Equal(t, math.MaxInt, g.InFlight(), "in-flight weight never goes negative")

	p.Release()
	require.Equal(t, 0, g.InFlight())
}
