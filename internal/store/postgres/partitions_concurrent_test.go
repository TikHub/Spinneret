package postgres_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/appconfig"
	"github.com/Evil0ctal/Spinneret/internal/store/postgres"
	"github.com/Evil0ctal/Spinneret/internal/testutil"
)

// TestPartitionManagerConcurrent runs several partition managers against the
// same database at once, as happens briefly during a leader hand-over: every
// call must succeed and the result must equal a single sequential run.
func TestPartitionManagerConcurrent(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx := integrationContext(t)
	pool := testutil.Postgres(t)
	// Far enough in the future that every partition has to be created.
	now := time.Date(2032, 7, 15, 12, 0, 0, 0, time.UTC)
	// Short enough to drop every partition created by the migration (years
	// before now), long enough to keep the whole EnsurePartitionWindow.
	const day = 24 * time.Hour
	retention := appconfig.Retention{
		RiskEvents: 2 * day, MinuteStats: 2 * day, HourStats: 45 * day, StateEvents: 45 * day, Audit: 45 * day,
	}

	const workers = 6
	var wg sync.WaitGroup
	errs := make([]error, workers)
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := postgres.EnsurePartitions(ctx, pool, now); err != nil {
				errs[i] = err
				return
			}
			errs[i] = postgres.DropExpiredPartitions(ctx, pool, retention, now)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		require.NoError(t, err, "worker %d", i)
	}

	for _, tbl := range postgres.PartitionedTables() {
		want := expectedPartitionNames(t, tbl.Name, tbl.Granularity, now)
		require.Equal(t, want, partitionNames(ctx, t, pool, tbl.Name),
			"%s must hold exactly the current window after concurrent runs", tbl.Name)
	}
}

// TestDropPartitionsBeforeWithoutExpiredPartitionsTakesNoLock verifies that
// the hourly retention run does not queue behind (or block) open transactions
// on a table when nothing has expired.
func TestDropPartitionsBeforeWithoutExpiredPartitionsTakesNoLock(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx := integrationContext(t)
	pool := testutil.Postgres(t)

	reader, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { require.NoError(t, reader.Rollback(ctx)) }()
	var n int
	require.NoError(t, reader.QueryRow(ctx, "SELECT count(*) FROM risk_events").Scan(&n))

	shortCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var dropped int
	cutoff := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, pool.QueryRow(shortCtx,
		"SELECT spinneret_drop_partitions_before('risk_events', 'day', $1)", cutoff).Scan(&dropped))
	require.Zero(t, dropped)
}

// TestEnsurePartitionsRejectsUnattachedNamesake verifies that a relation that
// carries a managed partition name but is not attached to the parent is
// reported instead of silently leaving the range uncovered.
func TestEnsurePartitionsRejectsUnattachedNamesake(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx := integrationContext(t)
	pool := testutil.Postgres(t)
	from := time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)

	var created int
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT spinneret_ensure_partitions('risk_events', 'day', $1, $1)", from).Scan(&created))
	require.Equal(t, 1, created)
	_, err := pool.Exec(ctx, "ALTER TABLE risk_events DETACH PARTITION risk_events_p20310101")
	require.NoError(t, err)

	_, err = pool.Exec(ctx, "SELECT spinneret_ensure_partitions('risk_events', 'day', $1, $1)", from)
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	require.Equal(t, "42P07", pgErr.Code)
	require.Contains(t, pgErr.Message, "is not a partition of")

	// After the stray table is removed the partition is created again.
	_, err = pool.Exec(ctx, "DROP TABLE risk_events_p20310101")
	require.NoError(t, err)
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT spinneret_ensure_partitions('risk_events', 'day', $1, $1)", from).Scan(&created))
	require.Equal(t, 1, created)
}
