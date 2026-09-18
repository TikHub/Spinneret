// Package jobs runs periodic background jobs. A job either runs on every
// instance or only on the elected leader; leadership is a PostgreSQL session
// advisory lock held on a dedicated connection, so it is released
// automatically when the holder dies.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Mode selects where a job runs.
type Mode int

const (
	// EachInstance runs the job on every instance.
	EachInstance Mode = iota
	// Leader runs the job only on the instance holding the job's advisory lock.
	Leader
)

// String implements fmt.Stringer.
func (m Mode) String() string {
	if m == Leader {
		return "leader"
	}
	return "each_instance"
}

// Job is a periodic unit of work. Run performs one iteration and must return
// when ctx is canceled.
type Job struct {
	Name     string
	Interval time.Duration
	Mode     Mode
	// Timeout bounds one iteration; zero means Interval (minimum 1s).
	Timeout time.Duration
	// InitialDelay delays the first iteration; zero means a random delay in [0, Interval).
	InitialDelay time.Duration
	Run          func(ctx context.Context) error
}

// Validate checks the job definition.
func (j Job) Validate() error {
	switch {
	case j.Name == "":
		return errors.New("jobs: job name is required")
	case j.Interval <= 0:
		return fmt.Errorf("jobs: job %s: interval must be positive", j.Name)
	case j.Mode != EachInstance && j.Mode != Leader:
		return fmt.Errorf("jobs: job %s: unknown mode %d", j.Name, j.Mode)
	case j.Timeout < 0:
		return fmt.Errorf("jobs: job %s: timeout must not be negative", j.Name)
	case j.InitialDelay < 0:
		return fmt.Errorf("jobs: job %s: initial delay must not be negative", j.Name)
	case j.Run == nil:
		return fmt.Errorf("jobs: job %s: run function is required", j.Name)
	}
	return nil
}

// LockFactory acquires leader locks. The returned release function must be
// called when leadership is no longer needed; it is safe to call more than
// once.
type LockFactory interface {
	// TryLock attempts to acquire the named lock without blocking. When ok is
	// false the lock is held elsewhere. lost is closed if the lock is lost
	// (for example because the underlying connection broke).
	TryLock(ctx context.Context, name string) (ok bool, lost <-chan struct{}, release func(), err error)
}

// Metrics receives job execution observations. All methods must be cheap.
type Metrics interface {
	ObserveJob(name string, d time.Duration, err error)
}

// Runner schedules jobs.
type Runner struct {
	locks   LockFactory
	logger  *slog.Logger
	metrics Metrics

	mu      sync.Mutex
	jobs    []Job
	started bool
}

// NewRunner creates a runner. locks may be nil when no Leader jobs are added.
func NewRunner(locks LockFactory, logger *slog.Logger, metrics Metrics) *Runner {
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{locks: locks, logger: logger, metrics: metrics}
}

// Add registers a job. It must be called before Run.
func (r *Runner) Add(j Job) error {
	if err := j.Validate(); err != nil {
		return err
	}
	if j.Mode == Leader && r.locks == nil {
		return fmt.Errorf("jobs: job %s requires a lock factory", j.Name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return fmt.Errorf("jobs: cannot add job %s after Run", j.Name)
	}
	for _, existing := range r.jobs {
		if existing.Name == j.Name {
			return fmt.Errorf("jobs: duplicate job %s", j.Name)
		}
	}
	r.jobs = append(r.jobs, j)
	return nil
}

// MustAdd is Add that panics on invalid definitions; intended for wiring code
// where a failure is a programming error.
func (r *Runner) MustAdd(j Job) {
	if err := r.Add(j); err != nil {
		panic(err)
	}
}

// Run executes all registered jobs until ctx is canceled.
func (r *Runner) Run(ctx context.Context) error {
	r.mu.Lock()
	r.started = true
	jobs := append([]Job(nil), r.jobs...)
	r.mu.Unlock()

	var wg sync.WaitGroup
	for _, j := range jobs {
		wg.Add(1)
		go func(j Job) {
			defer wg.Done()
			if j.Mode == Leader {
				r.runLeader(ctx, j)
				return
			}
			r.loop(ctx, j, nil)
		}(j)
	}
	wg.Wait()
	return nil
}

// runLeader repeatedly tries to become leader for the job and runs the job
// loop while leadership is held.
func (r *Runner) runLeader(ctx context.Context, j Job) {
	retry := j.Interval
	if retry > 10*time.Second {
		retry = 10 * time.Second
	}
	if retry < time.Second {
		retry = time.Second
	}
	for {
		ok, lost, release, err := r.locks.TryLock(ctx, "spinneret:job:"+j.Name)
		if err != nil && ctx.Err() == nil {
			r.logger.Warn("job leader lock failed", slog.String("job", j.Name), slog.Any("error", err))
		}
		if ok {
			r.logger.Debug("job leadership acquired", slog.String("job", j.Name))
			r.loop(ctx, j, lost)
			release()
			r.logger.Debug("job leadership released", slog.String("job", j.Name))
		}
		if !sleepCtx(ctx, jitter(retry)) {
			return
		}
	}
}

// loop runs iterations until ctx is canceled or lost is closed. Closing lost
// also cancels the iteration in progress, so that a leader job never keeps
// running (for up to its timeout) after another instance may have taken the
// lock over.
func (r *Runner) loop(ctx context.Context, j Job, lost <-chan struct{}) {
	if lost != nil {
		lctx, cancel := context.WithCancel(ctx)
		defer cancel()
		go func() {
			select {
			case <-lost:
				r.logger.Warn("job leadership lost", slog.String("job", j.Name))
				cancel()
			case <-lctx.Done():
			}
		}()
		ctx = lctx
	}
	delay := j.InitialDelay
	if delay == 0 {
		delay = time.Duration(rand.Int64N(int64(j.Interval))) //nolint:gosec // scheduling jitter, not security sensitive
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if ctx.Err() != nil {
			// Canceled (or leadership lost) while the timer fired.
			return
		}
		r.iterate(ctx, j)
		timer.Reset(j.Interval)
	}
}

func (r *Runner) iterate(ctx context.Context, j Job) {
	timeout := j.Timeout
	if timeout <= 0 {
		timeout = j.Interval
	}
	if timeout < time.Second {
		timeout = time.Second
	}
	ictx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	err := safeRun(ictx, j.Run)
	if r.metrics != nil {
		r.metrics.ObserveJob(j.Name, time.Since(start), err)
	}
	if err != nil && ctx.Err() == nil {
		r.logger.Error("job iteration failed", slog.String("job", j.Name), slog.Any("error", err))
	}
}

func safeRun(ctx context.Context, fn func(context.Context) error) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("jobs: panic: %v", rec)
		}
	}()
	return fn(ctx)
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	return d/2 + time.Duration(rand.Int64N(int64(d)/2+1)) //nolint:gosec // scheduling jitter, not security sensitive
}

// lockProbeTimeout bounds one liveness probe of a held lock's connection and
// the unlock performed on release.
const lockProbeTimeout = 3 * time.Second

// PGLocks implements LockFactory with PostgreSQL session advisory locks. Each
// held lock pins one pooled connection.
type PGLocks struct {
	pool *pgxpool.Pool
	// checkEvery controls how often a held lock's connection is probed.
	checkEvery time.Duration
}

// NewPGLocks creates a PostgreSQL advisory lock factory.
func NewPGLocks(pool *pgxpool.Pool) *PGLocks {
	return &PGLocks{pool: pool, checkEvery: 5 * time.Second}
}

// LockKey maps a lock name to a 64-bit advisory lock key.
func LockKey(name string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(name))
	return int64(h.Sum64())
}

// TryLock implements LockFactory. While the lock is held, its connection is
// pinged every checkEvery; when a ping fails, lost is closed. The returned
// release function stops the probe, unlocks (or, when the connection is
// broken or the unlock fails, closes the connection, which ends the session
// and its locks) and returns the connection to the pool. It is idempotent and
// safe for concurrent use.
func (l *PGLocks) TryLock(ctx context.Context, name string) (bool, <-chan struct{}, func(), error) {
	conn, err := l.pool.Acquire(ctx)
	if err != nil {
		return false, nil, nil, fmt.Errorf("acquire connection for lock %s: %w", name, err)
	}
	key := LockKey(name)
	var ok bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&ok); err != nil {
		// The lock may have been granted before the error (for example a
		// cancellation while reading the reply): end the session rather than
		// returning a connection that might hold it to the pool.
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lockProbeTimeout)
		_ = conn.Conn().Close(cctx)
		cancel()
		conn.Release()
		return false, nil, nil, fmt.Errorf("try advisory lock %s: %w", name, err)
	}
	if !ok {
		conn.Release()
		return false, nil, nil, nil
	}
	lost := make(chan struct{})
	stop := make(chan struct{})
	done := make(chan struct{})
	// The probe outlives TryLock's ctx (which only bounds the acquisition); it
	// stops when release is called.
	go func() { //nolint:gosec // G118: intentionally detached from the acquisition context
		defer close(done)
		t := time.NewTicker(l.checkEvery)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				pctx, cancel := context.WithTimeout(context.Background(), lockProbeTimeout)
				err := conn.Ping(pctx)
				cancel()
				if err != nil {
					close(lost)
					return
				}
			}
		}
	}()
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			close(stop)
			<-done
			uctx, cancel := context.WithTimeout(context.Background(), lockProbeTimeout)
			defer cancel()
			select {
			case <-lost:
				// The connection is broken; destroy it so the session (and lock) ends.
				_ = conn.Conn().Close(uctx)
			default:
				if _, err := conn.Exec(uctx, "SELECT pg_advisory_unlock($1)", key); err != nil {
					_ = conn.Conn().Close(uctx)
				}
			}
			conn.Release()
		})
	}
	return true, lost, release, nil
}
