package audit

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/testutil"
)

// storedEntry is an audit_logs row as read back by the tests.
type storedEntry struct {
	ActorKind    string
	ActorName    string
	Action       string
	ResourceName string
	UserAgent    string
	Details      []byte
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	return ctx
}

// runWriter starts w.Run and returns a function that stops it and waits for
// the final drain.
func runWriter(t *testing.T, w *Writer) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	var stopped bool
	stop = func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		require.NoError(t, <-done)
	}
	t.Cleanup(stop)
	return stop
}

func storedEntries(ctx context.Context, t *testing.T, pool *pgxpool.Pool) map[string]storedEntry {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT action, actor_kind, actor_name, resource_name, user_agent, details::text
		FROM audit_logs ORDER BY created_at, id`)
	require.NoError(t, err)
	out := map[string]storedEntry{}
	for rows.Next() {
		var e storedEntry
		var details string
		require.NoError(t, rows.Scan(&e.Action, &e.ActorKind, &e.ActorName, &e.ResourceName, &e.UserAgent, &details))
		e.Details = []byte(details)
		out[e.Action] = e
	}
	require.NoError(t, rows.Err())
	return out
}

func newTestWriter(t *testing.T) (*Writer, *pgxpool.Pool) {
	t.Helper()
	pool := testutil.Postgres(t)
	w := NewWriter(pool, slog.New(slog.DiscardHandler))
	w.interval = 20 * time.Millisecond
	return w, pool
}

// TestWriterSanitizesTextAndDetails is the regression test for a NUL byte in
// a login username: PostgreSQL rejects NUL in text and \u0000 in jsonb, which
// failed the COPY of the whole batch and lost every entry flushed with it.
func TestWriterSanitizesTextAndDetails(t *testing.T) {
	t.Parallel()
	ctx := testContext(t)
	w, pool := newTestWriter(t)
	stop := runWriter(t, w)

	w.Record(ctx, Entry{ActorKind: "user", ActorName: "operator", Action: "secret.reveal", ResourceName: "db/password"})
	w.Record(ctx, Entry{
		ActorKind: "user", ActorName: "evil\x00name", Action: "auth.login", ResourceName: "evil\x00name",
		Result: ResultDenied, UserAgent: "agent\xff\xfe/1",
		Details: map[string]any{
			"reason":   "throttled\x00",
			"nul\x00":  []any{"a\x00b", map[string]any{"deep": "x\x00"}},
			"invalid":  "bad\xffutf8",
			"counters": []int{1, 2},
		},
	})
	w.Record(ctx, Entry{ActorKind: "system", Action: "identity.import"})
	stop()

	got := storedEntries(ctx, t, pool)
	require.Len(t, got, 3, "no entry of the batch may be lost")
	require.Contains(t, got, "secret.reveal")
	require.Contains(t, got, "identity.import")
	login := got["auth.login"]
	require.Equal(t, "evil\uFFFDname", login.ActorName)
	require.Equal(t, "evil\uFFFDname", login.ResourceName)
	require.Equal(t, "agent\uFFFD/1", login.UserAgent, "a run of invalid bytes becomes one replacement character")
	var details map[string]any
	require.NoError(t, json.Unmarshal(login.Details, &details))
	require.Equal(t, map[string]any{
		"reason":    "throttled\uFFFD",
		"nul\uFFFD": []any{"a\uFFFDb", map[string]any{"deep": "x\uFFFD"}},
		"invalid":   "bad\uFFFDutf8",
		"counters":  []any{float64(1), float64(2)},
	}, details)
	require.Zero(t, w.Dropped())
}

// TestWriterIsolatesRejectedRows checks that a row the database rejects only
// loses itself, not the other entries of its batch.
func TestWriterIsolatesRejectedRows(t *testing.T) {
	t.Parallel()
	ctx := testContext(t)
	w, pool := newTestWriter(t)
	w.interval = time.Hour // everything is written by the final drain as one batch
	stop := runWriter(t, w)

	w.Record(ctx, Entry{ActorKind: "user", Action: "a.first"})
	// actor_kind violates audit_logs_actor_kind_check.
	w.Record(ctx, Entry{ActorKind: "robot", Action: "a.rejected"})
	w.Record(ctx, Entry{ActorKind: "token", Action: "a.second"})
	// No partition exists for this period.
	w.Record(ctx, Entry{ActorKind: "user", Action: "a.no_partition", CreatedAt: time.Date(1990, 1, 1, 0, 0, 0, 0, time.UTC)})
	w.Record(ctx, Entry{ActorKind: "system", Action: "a.third"})
	stop()

	got := storedEntries(ctx, t, pool)
	require.Len(t, got, 3)
	for _, action := range []string{"a.first", "a.second", "a.third"} {
		require.Contains(t, got, action)
	}
	require.EqualValues(t, 2, w.Dropped())
}

// TestWriterFlushSurvivesCancellation checks that a batch being flushed when
// Run's context is canceled is still written (the write is not bound to the
// canceled context).
func TestWriterFlushSurvivesCancellation(t *testing.T) {
	t.Parallel()
	ctx := testContext(t)
	w, pool := newTestWriter(t)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	kept := w.flush(canceled, []record{newRecord(Entry{ActorKind: "user", Action: "a.inflight"})})
	require.Empty(t, kept)
	var n int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE action = 'a.inflight'`).Scan(&n))
	require.Equal(t, 1, n)
}

// TestWriterRetriesTransientErrors checks that a batch whose write fails with
// a connection-level error is kept for the next flush instead of being lost.
func TestWriterRetriesTransientErrors(t *testing.T) {
	t.Parallel()
	ctx := testContext(t)
	w, pool := newTestWriter(t)
	w.retryBackoff = time.Millisecond
	failures := 2
	w.copyFrom = func(ctx context.Context, rows [][]any) error {
		if failures > 0 {
			failures--
			return pgx.ErrTxClosed // not a *pgconn.PgError: treated as a connection failure
		}
		return w.copyRows(ctx, rows)
	}
	kept := w.flush(ctx, []record{newRecord(Entry{ActorKind: "user", Action: "a.retried"})})
	require.Empty(t, kept)
	var n int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE action = 'a.retried'`).Scan(&n))
	require.Equal(t, 1, n)
	require.Zero(t, w.Dropped())
}

func TestCleanText(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"":                 "",
		"plain":            "plain",
		"nul\x00":          "nul\uFFFD",
		"\x00\x00":         "\uFFFD\uFFFD",
		"bad\xff":          "bad\uFFFD",
		"mixed\xff\x00end": "mixed\uFFFD\uFFFDend",
		"ok ünïcode":       "ok ünïcode",
	}
	for in, want := range tests {
		require.Equal(t, want, cleanText(in), "%q", in)
	}
}

func TestEncodeDetails(t *testing.T) {
	t.Parallel()
	require.Equal(t, "{}", string(encodeDetails(nil)))
	require.Equal(t, "{}", string(encodeDetails(map[string]any{})))
	require.JSONEq(t, `{"a":1,"b":"x"}`, string(encodeDetails(map[string]any{"a": 1, "b": "x"})))
	// A value json cannot encode yields an empty object instead of failing the row.
	require.Equal(t, "{}", string(encodeDetails(map[string]any{"ch": make(chan int)})))
	// A literal backslash followed by "u0000" is not an escaped NUL.
	require.JSONEq(t, `{"path":"C:\\u0000"}`, string(encodeDetails(map[string]any{"path": `C:\u0000`})))
	// Typed values (structs, typed slices) are cleaned too.
	type nested struct {
		Name string `json:"name"`
	}
	require.JSONEq(t, `{"n":{"name":"a\uFFFD"},"s":["\uFFFD"]}`,
		string(encodeDetails(map[string]any{"n": nested{Name: "a\x00"}, "s": []string{"\x00"}})))
}
