package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// sqlStateProgramLimitExceeded is raised for values too large to store (for
// example an index row above the page size); it concerns only those rows.
const sqlStateProgramLimitExceeded = "54000"

var auditColumns = []string{
	"id", "created_at", "tenant_id", "namespace_id", "actor_kind", "actor_id", "actor_name",
	"action", "resource_kind", "resource_id", "resource_name", "result", "ip", "user_agent", "details",
}

// errRetryInterrupted marks a transient write failure whose retries were cut
// short because the flush context ended.
var errRetryInterrupted = errors.New("audit: write retry interrupted")

// rejections accumulates the entries of one flush that the database rejected,
// so that they are logged once.
type rejections struct {
	count int
	err   error
	// action is the action of the first rejected entry.
	action string
}

// add records one rejected entry.
func (r *rejections) add(rec record, err error) {
	if r.count == 0 {
		r.err, r.action = err, rec.action
	}
	r.count++
}

// writeBatch persists recs and returns the entries kept for a later flush
// (see write). Rejected entries are counted as dropped and logged once.
func (w *Writer) writeBatch(ctx context.Context, recs []record) (kept []record) {
	var rej rejections
	kept = w.write(ctx, recs, &rej)
	if rej.count > 0 {
		w.lose(rej.count, "audit entries rejected by the database, dropping them", rej.err,
			slog.String("first_action", rej.action))
	}
	return kept
}

// write persists recs. A batch rejected because of its values is split in
// halves until the rejected entries are isolated (and added to rej); a batch
// that keeps failing for another reason is dropped as a whole. It returns the
// entries kept for a later flush because ctx ended while a transient failure
// was being retried.
func (w *Writer) write(ctx context.Context, recs []record, rej *rejections) (kept []record) {
	err := w.copyWithRetry(ctx, recs)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errRetryInterrupted):
		return recs
	case isDataError(err) && len(recs) > 1:
		mid := len(recs) / 2
		// Concat allocates: returning a sub-slice of recs would let the
		// caller's appends overwrite entries of the second half.
		return slices.Concat(w.write(ctx, recs[:mid], rej), w.write(ctx, recs[mid:], rej))
	case isDataError(err):
		rej.add(recs[0], err)
		return nil
	default:
		w.lose(len(recs), "audit flush failed, dropping entries", err)
		return nil
	}
}

// copyWithRetry writes recs with one COPY statement, retrying transient
// failures with exponential backoff. The statement itself is not canceled
// with ctx (a batch in flight at shutdown is still written); ctx only ends
// the waits between attempts and bounds the statement by its deadline.
func (w *Writer) copyWithRetry(ctx context.Context, recs []record) error {
	rows := make([][]any, len(recs))
	for i, r := range recs {
		rows[i] = r.values()
	}
	backoff := w.retryBackoff
	for attempt := 1; ; attempt++ {
		err := w.copyOnce(ctx, rows)
		if err == nil || !isRetryable(err) || attempt >= w.maxAttempts {
			return err
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("%w: %w", errRetryInterrupted, err)
		case <-timer.C:
		}
		backoff = min(backoff*2, maxRetryBackoff)
	}
}

// copyOnce runs one COPY statement bounded by the statement timeout and by
// ctx's deadline, but not canceled by ctx.
func (w *Writer) copyOnce(ctx context.Context, rows [][]any) error {
	timeout := w.statementTimeout
	if deadline, ok := ctx.Deadline(); ok {
		timeout = min(timeout, time.Until(deadline))
	}
	if timeout <= 0 {
		return fmt.Errorf("write audit entries: %w", context.DeadlineExceeded)
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	return w.copyFrom(sctx, rows)
}

// copyRows is the default copyFrom.
func (w *Writer) copyRows(ctx context.Context, rows [][]any) error {
	if _, err := w.pool.CopyFrom(ctx, pgx.Identifier{"audit_logs"}, auditColumns, pgx.CopyFromRows(rows)); err != nil {
		return fmt.Errorf("copy audit entries: %w", err)
	}
	return nil
}

// lose counts n entries as dropped and logs why.
func (w *Writer) lose(n int, msg string, err error, attrs ...slog.Attr) {
	if n <= 0 {
		return
	}
	total := w.dropped.Add(int64(n))
	args := make([]any, 0, len(attrs)+3)
	args = append(args, slog.Int("entries", n), slog.Int64("dropped_total", total), slog.Any("error", err))
	for _, a := range attrs {
		args = append(args, a)
	}
	w.logger.Error(msg, args...)
}

// isDataError reports whether err is caused by the written values (SQLSTATE
// classes 22 and 23, including a missing partition for a row's period, and
// program limit exceeded), so that retrying the same rows cannot succeed.
func isDataError(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return strings.HasPrefix(pgErr.Code, "22") || strings.HasPrefix(pgErr.Code, "23") ||
		pgErr.Code == sqlStateProgramLimitExceeded
}

// isRetryable reports whether retrying the same statement may succeed:
// client-side failures (connection loss, timeouts) and server errors of the
// SQLSTATE classes 08 (connection exception), 40 (transaction rollback), 53
// (insufficient resources), 55 (object not in prerequisite state), 57
// (operator intervention) and 58 (system error).
func isRetryable(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return true
	}
	if len(pgErr.Code) < 2 {
		return false
	}
	switch pgErr.Code[:2] {
	case "08", "40", "53", "55", "57", "58":
		return true
	default:
		return false
	}
}
