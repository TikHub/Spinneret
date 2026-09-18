package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/store/postgres"
	"github.com/TikHub/Spinneret/internal/store/postgres/db"
	"github.com/TikHub/Spinneret/internal/testutil"
)

func tenantExists(ctx context.Context, t *testing.T, pool *pgxpool.Pool, id string) bool {
	t.Helper()
	var exists bool
	require.NoError(t, pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM tenants WHERE id = $1)", id).Scan(&exists))
	return exists
}

func insertTenant(ctx context.Context, tx pgx.Tx, id string) error {
	_, err := tx.Exec(ctx, "INSERT INTO tenants (id, name) VALUES ($1, $1)", id)
	return err
}

func TestInTx(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx := integrationContext(t)
	pool := testutil.Postgres(t)

	t.Run("commit", func(t *testing.T) {
		err := postgres.InTx(ctx, pool, func(tx pgx.Tx, q *db.Queries) error {
			var level string
			if err := tx.QueryRow(ctx, "SHOW transaction_isolation").Scan(&level); err != nil {
				return err
			}
			require.Equal(t, "read committed", level)
			ok, err := q.Ping(ctx)
			if err != nil {
				return err
			}
			require.Equal(t, int32(1), ok)
			return insertTenant(ctx, tx, "ten_commit")
		})
		require.NoError(t, err)
		require.True(t, tenantExists(ctx, t, pool, "ten_commit"))
	})

	t.Run("error rolls back and is returned unchanged", func(t *testing.T) {
		sentinel := apperr.FailedPrecondition("", "stop")
		err := postgres.InTx(ctx, pool, func(tx pgx.Tx, _ *db.Queries) error {
			if err := insertTenant(ctx, tx, "ten_rollback"); err != nil {
				return err
			}
			return sentinel
		})
		require.Same(t, sentinel, err)
		require.False(t, tenantExists(ctx, t, pool, "ten_rollback"))
	})

	t.Run("panic rolls back and propagates", func(t *testing.T) {
		require.PanicsWithValue(t, "boom", func() {
			_ = postgres.InTx(ctx, pool, func(tx pgx.Tx, _ *db.Queries) error {
				if err := insertTenant(ctx, tx, "ten_panic"); err != nil {
					return err
				}
				panic("boom")
			})
		})
		require.False(t, tenantExists(ctx, t, pool, "ten_panic"))
		// The pool is still healthy afterwards.
		require.NoError(t, pool.Ping(ctx))
	})

	t.Run("database error can be mapped", func(t *testing.T) {
		err := postgres.InTx(ctx, pool, func(tx pgx.Tx, _ *db.Queries) error {
			return insertTenant(ctx, tx, "ten_commit") // duplicate primary key
		})
		require.True(t, errors.Is(postgres.MapError(err, "tenant"), err))
		appErr, ok := apperr.As(postgres.MapError(err, "tenant"))
		require.True(t, ok)
		require.Equal(t, apperr.ReasonAlreadyExists, appErr.Reason)
	})

	t.Run("commit failure", func(t *testing.T) {
		// A deferred constraint is only checked at COMMIT.
		_, err := pool.Exec(ctx, `CREATE TABLE tx_deferred (
			id int, CONSTRAINT tx_deferred_id_key UNIQUE (id) DEFERRABLE INITIALLY DEFERRED)`)
		require.NoError(t, err)
		err = postgres.InTx(ctx, pool, func(tx pgx.Tx, _ *db.Queries) error {
			_, err := tx.Exec(ctx, "INSERT INTO tx_deferred (id) VALUES (1), (1)")
			return err
		})
		require.ErrorContains(t, err, "commit transaction")
		appErr, ok := apperr.As(postgres.MapError(err, "row"))
		require.True(t, ok)
		require.Equal(t, apperr.ReasonAlreadyExists, appErr.Reason)
	})

	t.Run("begin failure", func(t *testing.T) {
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		called := false
		err := postgres.InTx(canceled, pool, func(pgx.Tx, *db.Queries) error {
			called = true
			return nil
		})
		require.ErrorContains(t, err, "begin transaction")
		require.ErrorIs(t, err, context.Canceled)
		require.False(t, called)
	})
}

func TestInTxInvalidArguments(t *testing.T) {
	err := postgres.InTx(context.Background(), nil, func(pgx.Tx, *db.Queries) error { return nil })
	require.ErrorContains(t, err, "pool is nil")
	err = postgres.InTx(context.Background(), &pgxpool.Pool{}, nil)
	require.ErrorContains(t, err, "fn is nil")
}
