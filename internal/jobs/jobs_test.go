package jobs

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// startRunner runs r until the returned stop function is called; stop waits
// for Run to return.
func startRunner(t *testing.T, r *Runner) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(10 * time.Second):
				t.Fatal("Runner.Run did not return after cancel")
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

// recordingMetrics records job observations.
type recordingMetrics struct {
	mu   sync.Mutex
	errs map[string][]error
}

func (m *recordingMetrics) ObserveJob(name string, d time.Duration, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.errs == nil {
		m.errs = map[string][]error{}
	}
	m.errs[name] = append(m.errs[name], err)
}

func (m *recordingMetrics) observations(name string) []error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]error(nil), m.errs[name]...)
}

// fakeLocks is an in-memory LockFactory: one holder per name, and tests can
// break a held lock by closing its lost channel.
type fakeLocks struct {
	mu       sync.Mutex
	held     map[string]chan struct{}
	attempts atomic.Int64
	releases atomic.Int64
	err      error
}

func newFakeLocks() *fakeLocks { return &fakeLocks{held: map[string]chan struct{}{}} }

func (f *fakeLocks) TryLock(_ context.Context, name string) (bool, <-chan struct{}, func(), error) {
	f.attempts.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return false, nil, nil, f.err
	}
	if _, ok := f.held[name]; ok {
		return false, nil, nil, nil
	}
	lost := make(chan struct{})
	f.held[name] = lost
	var once sync.Once
	return true, lost, func() {
		once.Do(func() {
			f.releases.Add(1)
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.held[name] == lost {
				delete(f.held, name)
			}
		})
	}, nil
}

// breakLock closes the lost channel of a held lock (the holder must release it).
func (f *fakeLocks) breakLock(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	lost, ok := f.held[name]
	if ok {
		close(lost)
	}
	return ok
}

func (f *fakeLocks) isHeld(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.held[name]
	return ok
}

func TestModeString(t *testing.T) {
	require.Equal(t, "each_instance", EachInstance.String())
	require.Equal(t, "leader", Leader.String())
}

func TestJobValidate(t *testing.T) {
	run := func(context.Context) error { return nil }
	tests := []struct {
		name string
		job  Job
		want string
	}{
		{"valid each instance", Job{Name: "a", Interval: time.Second, Run: run}, ""},
		{"valid leader with options", Job{Name: "a", Interval: time.Second, Mode: Leader, Timeout: time.Minute, InitialDelay: time.Millisecond, Run: run}, ""},
		{"missing name", Job{Interval: time.Second, Run: run}, "job name is required"},
		{"zero interval", Job{Name: "a", Run: run}, "interval must be positive"},
		{"negative interval", Job{Name: "a", Interval: -time.Second, Run: run}, "interval must be positive"},
		{"unknown mode", Job{Name: "a", Interval: time.Second, Mode: Mode(7), Run: run}, "unknown mode"},
		{"negative timeout", Job{Name: "a", Interval: time.Second, Timeout: -1, Run: run}, "timeout must not be negative"},
		{"negative initial delay", Job{Name: "a", Interval: time.Second, InitialDelay: -1, Run: run}, "initial delay must not be negative"},
		{"missing run", Job{Name: "a", Interval: time.Second}, "run function is required"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.job.Validate()
			if tc.want == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestRunnerAdd(t *testing.T) {
	run := func(context.Context) error { return nil }

	r := NewRunner(nil, nil, nil)
	require.NotNil(t, r.logger, "nil logger defaults")
	require.NoError(t, r.Add(Job{Name: "a", Interval: time.Hour, Run: run}))
	require.ErrorContains(t, r.Add(Job{Name: "a", Interval: time.Hour, Run: run}), "duplicate job a")
	require.ErrorContains(t, r.Add(Job{Name: "b", Interval: time.Hour, Mode: Leader, Run: run}), "requires a lock factory")
	require.ErrorContains(t, r.Add(Job{Name: "c", Run: run}), "interval must be positive")

	stop := startRunner(t, r)
	require.Eventually(t, func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.started
	}, 5*time.Second, time.Millisecond)
	require.ErrorContains(t, r.Add(Job{Name: "late", Interval: time.Hour, Run: run}), "cannot add job late after Run")
	stop()
}

func TestRunnerMustAdd(t *testing.T) {
	r := NewRunner(newFakeLocks(), discardLogger(), nil)
	require.NotPanics(t, func() {
		r.MustAdd(Job{Name: "a", Interval: time.Hour, Mode: Leader, Run: func(context.Context) error { return nil }})
	})
	require.PanicsWithError(t, "jobs: duplicate job a", func() {
		r.MustAdd(Job{Name: "a", Interval: time.Hour, Run: func(context.Context) error { return nil }})
	})
	require.Panics(t, func() { r.MustAdd(Job{}) })
}

func TestRunnerWithoutJobs(t *testing.T) {
	r := NewRunner(nil, discardLogger(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, r.Run(ctx))
}

func TestRunnerEachInstance(t *testing.T) {
	metrics := &recordingMetrics{}
	r := NewRunner(nil, discardLogger(), metrics)
	var fast, slow atomic.Int64
	deadlines := make(chan time.Duration, 1)
	r.MustAdd(Job{Name: "fast", Interval: 5 * time.Millisecond, InitialDelay: time.Millisecond, Run: func(ctx context.Context) error {
		if fast.Add(1) == 1 {
			remaining := time.Duration(-1)
			if dl, ok := ctx.Deadline(); ok {
				remaining = time.Until(dl)
			}
			deadlines <- remaining
		}
		return nil
	}})
	r.MustAdd(Job{Name: "failing", Interval: 5 * time.Millisecond, InitialDelay: time.Millisecond, Timeout: 2 * time.Second, Run: func(ctx context.Context) error {
		return errors.New("boom")
	}})
	r.MustAdd(Job{Name: "slow", Interval: time.Hour, InitialDelay: time.Hour, Run: func(context.Context) error {
		slow.Add(1)
		return nil
	}})
	stop := startRunner(t, r)

	require.Eventually(t, func() bool { return fast.Load() >= 5 }, 5*time.Second, time.Millisecond)
	require.Eventually(t, func() bool { return len(metrics.observations("failing")) >= 3 }, 5*time.Second, time.Millisecond)
	// Short intervals get the minimum iteration timeout of one second.
	dl := <-deadlines
	require.Greater(t, dl, 500*time.Millisecond)
	require.LessOrEqual(t, dl, time.Second)
	for _, err := range metrics.observations("failing") {
		require.EqualError(t, err, "boom")
	}
	for _, err := range metrics.observations("fast") {
		require.NoError(t, err)
	}
	stop()

	// No iteration runs after Run returned.
	after := fast.Load()
	time.Sleep(30 * time.Millisecond)
	require.Equal(t, after, fast.Load())
	require.Zero(t, slow.Load(), "the initial delay is honoured")
}

func TestRunnerIterationTimeout(t *testing.T) {
	r := NewRunner(nil, discardLogger(), nil)
	errs := make(chan error, 1)
	elapsed := make(chan time.Duration, 1)
	r.MustAdd(Job{Name: "blocking", Interval: time.Hour, InitialDelay: time.Millisecond, Timeout: 1500 * time.Millisecond,
		Run: func(ctx context.Context) error {
			started := time.Now()
			<-ctx.Done()
			errs <- ctx.Err()
			elapsed <- time.Since(started)
			return ctx.Err()
		}})
	stop := startRunner(t, r)
	select {
	case err := <-errs:
		require.ErrorIs(t, err, context.DeadlineExceeded)
		// An explicit timeout above the one-second minimum is used as is.
		require.Greater(t, <-elapsed, 1200*time.Millisecond)
	case <-time.After(5 * time.Second):
		t.Fatal("iteration was not canceled by its timeout")
	}
	stop()
}

func TestRunnerRecoversPanics(t *testing.T) {
	metrics := &recordingMetrics{}
	r := NewRunner(nil, discardLogger(), metrics)
	var calls atomic.Int64
	r.MustAdd(Job{Name: "panicky", Interval: 2 * time.Millisecond, InitialDelay: time.Millisecond, Run: func(context.Context) error {
		calls.Add(1)
		panic("kaboom")
	}})
	stop := startRunner(t, r)
	require.Eventually(t, func() bool { return calls.Load() >= 3 }, 5*time.Second, time.Millisecond, "the job keeps running after panics")
	stop()
	obs := metrics.observations("panicky")
	require.NotEmpty(t, obs)
	for _, err := range obs {
		require.EqualError(t, err, "jobs: panic: kaboom")
	}
}

func TestSafeRun(t *testing.T) {
	require.NoError(t, safeRun(context.Background(), func(context.Context) error { return nil }))
	require.EqualError(t, safeRun(context.Background(), func(context.Context) error { return errors.New("x") }), "x")
	err := safeRun(context.Background(), func(context.Context) error { panic(errors.New("wrapped")) })
	require.EqualError(t, err, "jobs: panic: wrapped")
}

func TestRunnerLeaderWithFakeLocks(t *testing.T) {
	locks := newFakeLocks()
	lockName := "spinneret:job:elect"
	var a, b atomic.Int64
	newLeaderRunner := func(counter *atomic.Int64) *Runner {
		r := NewRunner(locks, discardLogger(), nil)
		r.MustAdd(Job{Name: "elect", Interval: 2 * time.Millisecond, InitialDelay: time.Millisecond, Mode: Leader,
			Run: func(context.Context) error {
				counter.Add(1)
				return nil
			}})
		return r
	}
	stopA := startRunner(t, newLeaderRunner(&a))
	require.Eventually(t, func() bool { return a.Load() > 0 }, 5*time.Second, time.Millisecond)
	stopB := startRunner(t, newLeaderRunner(&b))
	require.Eventually(t, func() bool { return locks.attempts.Load() >= 2 }, 5*time.Second, time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	require.Zero(t, b.Load(), "only the lock holder runs iterations")

	// Losing the lock stops the leader's loop and releases the lock; the
	// other runner (or the same one) takes over on its next attempt.
	before := locks.releases.Load()
	require.True(t, locks.breakLock(lockName))
	require.Eventually(t, func() bool { return locks.releases.Load() > before }, 5*time.Second, time.Millisecond)
	require.Eventually(t, func() bool { return locks.isHeld(lockName) }, 5*time.Second, 10*time.Millisecond)

	stopA()
	stopB()
	require.False(t, locks.isHeld(lockName), "stopping the runners releases the lock")
}

// TestRunnerLeaderLostCancelsIteration checks that losing the lock cancels
// the iteration in progress instead of letting it run until its timeout
// while another instance may already be leader.
func TestRunnerLeaderLostCancelsIteration(t *testing.T) {
	locks := newFakeLocks()
	lockName := "spinneret:job:long"
	started := make(chan struct{}, 1)
	canceled := make(chan error, 1)
	r := NewRunner(locks, discardLogger(), nil)
	r.MustAdd(Job{Name: "long", Mode: Leader, Interval: time.Hour, InitialDelay: time.Millisecond,
		Run: func(ctx context.Context) error {
			select {
			case started <- struct{}{}:
			default:
			}
			<-ctx.Done()
			select {
			case canceled <- ctx.Err():
			default:
			}
			return ctx.Err()
		}})
	stop := startRunner(t, r)

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("leader iteration did not start")
	}
	before := locks.releases.Load()
	require.True(t, locks.breakLock(lockName))
	select {
	case err := <-canceled:
		require.ErrorIs(t, err, context.Canceled, "the iteration is canceled, not timed out")
	case <-time.After(5 * time.Second):
		t.Fatal("losing the lock did not cancel the running iteration")
	}
	require.Eventually(t, func() bool { return locks.releases.Load() > before }, 5*time.Second, time.Millisecond,
		"the lost lock is released")
	stop()
}

func TestRunnerLeaderLockErrors(t *testing.T) {
	locks := newFakeLocks()
	locks.err = errors.New("database down")
	r := NewRunner(locks, discardLogger(), nil)
	var calls atomic.Int64
	r.MustAdd(Job{Name: "elect", Interval: time.Millisecond, Mode: Leader, Run: func(context.Context) error {
		calls.Add(1)
		return nil
	}})
	stop := startRunner(t, r)
	require.Eventually(t, func() bool { return locks.attempts.Load() >= 1 }, 5*time.Second, time.Millisecond)
	stop()
	require.Zero(t, calls.Load())
}

func TestJitterAndSleep(t *testing.T) {
	require.Equal(t, time.Duration(0), jitter(0))
	require.Equal(t, -time.Second, jitter(-time.Second))
	for range 100 {
		d := jitter(time.Second)
		require.GreaterOrEqual(t, d, 500*time.Millisecond)
		require.LessOrEqual(t, d, time.Second)
	}

	require.True(t, sleepCtx(context.Background(), time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	require.False(t, sleepCtx(ctx, time.Hour))
	require.Less(t, time.Since(start), time.Second)
}

func TestLockKey(t *testing.T) {
	// FNV-1a 64 of the empty string is its offset basis.
	var basis uint64 = 0xcbf29ce484222325
	require.Equal(t, int64(basis), LockKey(""))
	require.Equal(t, LockKey("spinneret:job:partition_manager"), LockKey("spinneret:job:partition_manager"))
	seen := map[int64]string{}
	for _, name := range []string{"a", "b", "spinneret:job:a", "spinneret:job:b", strings.Repeat("x", 1000)} {
		k := LockKey(name)
		prev, dup := seen[k]
		require.False(t, dup, "%q and %q collide", name, prev)
		seen[k] = name
	}
}
