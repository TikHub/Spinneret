package postgres_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/store/postgres"
	"github.com/Evil0ctal/Spinneret/internal/testutil"
)

// insertAlerts inserts n alert events named <prefix>-<i> created at createdAt.
func insertAlerts(ctx context.Context, t *testing.T, pool *pgxpool.Pool, prefix string, n int, createdAt time.Time) {
	t.Helper()
	for i := range n {
		_, err := pool.Exec(ctx, `INSERT INTO alert_events (id, created_at, tenant_id, kind, severity, title)
			VALUES ($1, $2, 'ten_a', 'breaker_open', 'warning', 'breaker opened')`,
			fmt.Sprintf("%s-%03d", prefix, i), createdAt)
		require.NoError(t, err)
	}
}

func alertIDs(ctx context.Context, t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(ctx, "SELECT id FROM alert_events ORDER BY id")
	require.NoError(t, err)
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	return ids
}

func TestPurgeAlertEventsValidation(t *testing.T) {
	ctx := context.Background()
	now := time.Now()

	_, err := postgres.PurgeAlertEvents(ctx, nil, now, 10)
	require.ErrorContains(t, err, "pool is nil")

	// Argument checks run before the pool is used, so a closed pool is fine.
	pool, err := pgxpool.New(ctx, "postgres://invalid:invalid@127.0.0.1:1/none")
	require.NoError(t, err)
	pool.Close()

	_, err = postgres.PurgeAlertEvents(ctx, pool, time.Time{}, 10)
	require.ErrorContains(t, err, "cutoff time is zero")
	for _, batch := range []int{0, -1} {
		_, err = postgres.PurgeAlertEvents(ctx, pool, now, batch)
		require.ErrorContains(t, err, "batch must be positive")
	}
}

func TestPurgeAlertEvents(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx := integrationContext(t)
	pool := testutil.Postgres(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	cutoff := now.Add(-24 * time.Hour)

	insertAlerts(ctx, t, pool, "old", 7, cutoff.Add(-time.Hour))
	insertAlerts(ctx, t, pool, "edge", 1, cutoff) // created exactly at the cutoff: kept
	insertAlerts(ctx, t, pool, "new", 3, now)

	t.Run("deletes every expired row across batches", func(t *testing.T) {
		deleted, err := postgres.PurgeAlertEvents(ctx, pool, cutoff, 3)
		require.NoError(t, err)
		require.Equal(t, int64(7), deleted)
		require.Equal(t, []string{"edge-000", "new-000", "new-001", "new-002"}, alertIDs(ctx, t, pool))
	})

	t.Run("exact multiple of the batch size", func(t *testing.T) {
		insertAlerts(ctx, t, pool, "old2", 4, cutoff.Add(-time.Minute))
		deleted, err := postgres.PurgeAlertEvents(ctx, pool, cutoff, 2)
		require.NoError(t, err)
		require.Equal(t, int64(4), deleted)
	})

	t.Run("nothing to purge", func(t *testing.T) {
		deleted, err := postgres.PurgeAlertEvents(ctx, pool, cutoff, 100)
		require.NoError(t, err)
		require.Zero(t, deleted)
		require.Len(t, alertIDs(ctx, t, pool), 4)
	})

	t.Run("batch larger than the maximum is clamped", func(t *testing.T) {
		deleted, err := postgres.PurgeAlertEvents(ctx, pool, now.Add(time.Second), postgres.MaxPurgeBatch*10)
		require.NoError(t, err)
		require.Equal(t, int64(4), deleted)
		require.Empty(t, alertIDs(ctx, t, pool))
	})

	t.Run("canceled context", func(t *testing.T) {
		insertAlerts(ctx, t, pool, "old3", 2, cutoff.Add(-time.Hour))
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		deleted, err := postgres.PurgeAlertEvents(canceled, pool, cutoff, 1)
		require.ErrorIs(t, err, context.Canceled)
		require.Zero(t, deleted)
		require.Len(t, alertIDs(ctx, t, pool), 2)
	})

	t.Run("rows locked by another transaction are skipped", func(t *testing.T) {
		tx, err := pool.Begin(ctx)
		require.NoError(t, err)
		t.Cleanup(func() { _ = tx.Rollback(context.WithoutCancel(ctx)) })
		_, err = tx.Exec(ctx, "SELECT id FROM alert_events WHERE id = 'old3-000' FOR UPDATE")
		require.NoError(t, err)

		deleted, err := postgres.PurgeAlertEvents(ctx, pool, cutoff, 10)
		require.NoError(t, err)
		require.Equal(t, int64(1), deleted)
		require.Equal(t, []string{"old3-000"}, alertIDs(ctx, t, pool))

		require.NoError(t, tx.Rollback(ctx))
		deleted, err = postgres.PurgeAlertEvents(ctx, pool, cutoff, 10)
		require.NoError(t, err)
		require.Equal(t, int64(1), deleted)
		require.Empty(t, alertIDs(ctx, t, pool))
	})
}
