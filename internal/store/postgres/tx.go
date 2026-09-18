package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/TikHub/Spinneret/internal/store/postgres/db"
)

// rollbackTimeout bounds the rollback issued after a failed transaction. The
// rollback deliberately ignores cancellation of the caller's context so that
// the connection returns to the pool in a clean state.
const rollbackTimeout = 5 * time.Second

// InTx runs fn inside a READ COMMITTED transaction. The transaction is
// committed when fn returns nil and rolled back when fn returns an error or
// panics (the panic is propagated after the rollback). Errors returned by fn
// are returned unchanged so application errors keep their identity; begin and
// commit failures are wrapped and can be classified with MapError.
func InTx(ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx, q *db.Queries) error) error {
	if pool == nil {
		return errors.New("transaction: pool is nil")
	}
	if fn == nil {
		return errors.New("transaction: fn is nil")
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		// After a successful commit Rollback is a no-op (pgx.ErrTxClosed). A
		// failed rollback only means the connection is unusable; pgx closes
		// it, and the original error (or panic) is what matters.
		rbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
		defer cancel()
		_ = tx.Rollback(rbCtx)
	}()

	if err := fn(tx, db.New(tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}
