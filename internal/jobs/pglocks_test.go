package jobs

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/testutil"
)

// testLocks returns a PGLocks with a fast connection probe.
func testLocks(pool *pgxpool.Pool) *PGLocks {
	l := NewPGLocks(pool)
	l.checkEvery = 20 * time.Millisecond
	return l
}

func integrationContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	return ctx
}

// lockHolderPID returns the backend PID holding the advisory lock of name,
// or 0 when nobody holds it.
func lockHolderPID(ctx context.Context, t *testing.T, pool *pgxpool.Pool, name string) int32 {
	t.Helper()
	var pid int32
	err := pool.QueryRow(ctx, `
		SELECT coalesce(max(pid), 0) FROM pg_locks
		WHERE locktype = 'advisory' AND objsubid = 1 AND granted
		  AND classid::bigint = (($1::bigint >> 32) & 4294967295)
		  AND objid::bigint = ($1::bigint & 4294967295)`, LockKey(name)).Scan(&pid)
	require.NoError(t, err)
	return pid
}

func TestNewPGLocks(t *testing.T) {
	l := NewPGLocks(nil)
	require.Equal(t, 5*time.Second, l.checkEvery)
}

func TestPGLocksMutualExclusion(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx := integrationContext(t)
	pool := testutil.Postgres(t)
	instanceA, instanceB := testLocks(pool), testLocks(pool)

	ok, lost, release, err := instanceA.TryLock(ctx, "job:a")
	require.NoError(t, err)
	require.True(t, ok)
	require.NotNil(t, lost)
	require.NotZero(t, lockHolderPID(ctx, t, pool, "job:a"))

	// Another session cannot take it, even from the same factory.
	for _, l := range []*PGLocks{instanceA, instanceB} {
		ok2, lost2, release2, err := l.TryLock(ctx, "job:a")
		require.NoError(t, err)
		require.False(t, ok2)
		require.Nil(t, lost2)
		require.Nil(t, release2)
	}
	// Other names are independent.
	okB, _, releaseB, err := instanceB.TryLock(ctx, "job:b")
	require.NoError(t, err)
	require.True(t, okB)
	releaseB()

	select {
	case <-lost:
		t.Fatal("a healthy lock must not be reported lost")
	case <-time.After(100 * time.Millisecond):
	}

	// Release is idempotent and safe for concurrent use.
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(release)
	}
	wg.Wait()
	release()
	require.Zero(t, lockHolderPID(ctx, t, pool, "job:a"))
	require.Zero(t, pool.Stat().AcquiredConns(), "released connections return to the pool")

	ok, _, release, err = instanceB.TryLock(ctx, "job:a")
	require.NoError(t, err)
	require.True(t, ok, "a released lock can be taken by another instance")
	release()
}

func TestPGLocksLostConnection(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx := integrationContext(t)
	pool := testutil.Postgres(t)
	locks := testLocks(pool)

	ok, lost, release, err := locks.TryLock(ctx, "job:lost")
	require.NoError(t, err)
	require.True(t, ok)
	pid := lockHolderPID(ctx, t, pool, "job:lost")
	require.NotZero(t, pid)

	var terminated bool
	require.NoError(t, pool.QueryRow(ctx, "SELECT pg_terminate_backend($1)", pid).Scan(&terminated))
	require.True(t, terminated)

	select {
	case <-lost:
	case <-time.After(10 * time.Second):
		t.Fatal("lost was not closed after the lock connection died")
	}
	require.NotPanics(t, release)
	require.NotPanics(t, release)
	// The broken connection is destroyed asynchronously by the pool.
	require.Eventually(t, func() bool { return pool.Stat().AcquiredConns() == 0 }, 5*time.Second, 5*time.Millisecond)

	ok, _, release, err = locks.TryLock(ctx, "job:lost")
	require.NoError(t, err)
	require.True(t, ok, "the lock ended with its session")
	release()
}

func TestPGLocksErrors(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx := integrationContext(t)
	pool := testutil.Postgres(t)
	locks := testLocks(pool)

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	ok, lost, release, err := locks.TryLock(canceled, "job:err")
	require.Error(t, err)
	require.False(t, ok)
	require.Nil(t, lost)
	require.Nil(t, release)
	require.Eventually(t, func() bool { return pool.Stat().AcquiredConns() == 0 }, 5*time.Second, 5*time.Millisecond)
	require.Zero(t, lockHolderPID(ctx, t, pool, "job:err"))

	closed := testutil.Postgres(t)
	closed.Close()
	_, _, _, err = testLocks(closed).TryLock(ctx, "job:err")
	require.ErrorContains(t, err, "acquire connection for lock job:err")
}

// TestRunnerLeaderElectionPostgres runs two runners (two "instances" with
// separate pools) competing for one job lock in PostgreSQL: only one runs
// iterations, and the other takes over when the leader stops.
func TestRunnerLeaderElectionPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx := integrationContext(t)
	dsn := testutil.PostgresURL(t)
	openPool := func() *pgxpool.Pool {
		pool, err := pgxpool.New(ctx, dsn)
		require.NoError(t, err)
		t.Cleanup(pool.Close)
		return pool
	}
	poolA, poolB := openPool(), openPool()

	var countA, countB atomic.Int64
	newInstance := func(pool *pgxpool.Pool, counter *atomic.Int64) *Runner {
		r := NewRunner(testLocks(pool), discardLogger(), nil)
		r.MustAdd(Job{Name: "elect", Mode: Leader, Interval: 5 * time.Millisecond, InitialDelay: time.Millisecond,
			Run: func(context.Context) error {
				counter.Add(1)
				return nil
			}})
		return r
	}
	stopA := startRunner(t, newInstance(poolA, &countA))
	stopB := startRunner(t, newInstance(poolB, &countB))

	require.Eventually(t, func() bool { return countA.Load()+countB.Load() >= 10 }, 10*time.Second, 5*time.Millisecond)
	// Both runners have attempted the lock at least once by now (their first
	// attempt happens immediately); keep observing across a retry period.
	time.Sleep(1200 * time.Millisecond)
	a, b := countA.Load(), countB.Load()
	require.True(t, (a > 0) != (b > 0), "exactly one instance runs iterations (a=%d, b=%d)", a, b)

	leaderStop, follower, followerStop := stopA, &countB, stopB
	if b > 0 {
		leaderStop, follower, followerStop = stopB, &countA, stopA
	}
	leaderStop()
	require.Eventually(t, func() bool { return follower.Load() > 0 }, 10*time.Second, 5*time.Millisecond,
		"the follower takes over after the leader released the lock")
	followerStop()
	require.Zero(t, lockHolderPID(ctx, t, poolA, "spinneret:job:elect"), "stopped runners release the lock")
}
