package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// MaxPurgeBatch is the largest number of rows a single purge statement
// deletes. Larger batch sizes are clamped to it so that one statement never
// holds row locks or generates WAL for an unbounded number of rows.
const MaxPurgeBatch = 10000

// purgeAlertEventsSQL deletes the oldest expired alert events, at most $2 of
// them. Rows locked by a concurrent writer (for example a delivery status
// update) or by a concurrent purge are skipped and picked up by a later run.
const purgeAlertEventsSQL = `
DELETE FROM alert_events
WHERE id IN (
    SELECT id FROM alert_events
    WHERE created_at < $1
    ORDER BY created_at
    LIMIT $2
    FOR UPDATE SKIP LOCKED
)`

// PurgeAlertEvents deletes alert events created strictly before before, in
// batches of at most batch rows (clamped to MaxPurgeBatch), each batch in its
// own implicit transaction so that locks are short-lived. It returns the
// number of deleted rows, including the rows deleted before an error or a
// context cancellation stopped the purge. It is safe to run concurrently.
func PurgeAlertEvents(ctx context.Context, pool *pgxpool.Pool, before time.Time, batch int) (int64, error) {
	switch {
	case pool == nil:
		return 0, errors.New("purge alert events: pool is nil")
	case before.IsZero():
		return 0, errors.New("purge alert events: cutoff time is zero")
	case batch <= 0:
		return 0, fmt.Errorf("purge alert events: batch must be positive (got %d)", batch)
	}
	batch = min(batch, MaxPurgeBatch)
	cutoff := before.UTC()

	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, fmt.Errorf("purge alert events: %w", err)
		}
		tag, err := pool.Exec(ctx, purgeAlertEventsSQL, cutoff, batch)
		if err != nil {
			return total, fmt.Errorf("purge alert events: %w", err)
		}
		deleted := tag.RowsAffected()
		total += deleted
		// A short batch means no unlocked expired row is left.
		if deleted < int64(batch) {
			return total, nil
		}
	}
}
