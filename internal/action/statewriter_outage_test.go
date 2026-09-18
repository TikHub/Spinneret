package action

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/observability"
)

// flakyPool returns a pool to the test database whose new connections fail
// while down is set (existing connections are closed with Reset).
func flakyPool(t *testing.T, e *env, down *atomic.Bool) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(e.pool.Config().ConnString())
	require.NoError(t, err)
	dialer := &net.Dialer{Timeout: time.Second}
	cfg.ConnConfig.DialFunc = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if down.Load() {
			return nil, &net.OpError{Op: "dial", Net: network, Err: errors.New("connection refused")}
		}
		return dialer.DialContext(ctx, network, addr)
	}
	cfg.ConnConfig.ConnectTimeout = time.Second
	pool, err := pgxpool.NewWithConfig(e.ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

func TestStateWriterKeepsChangesWhilePostgresUnavailable(t *testing.T) {
	e := newEnv(t, true)
	e.addIdentity(identitySeed{ID: "idt_1", Key: 101, NoRedis: true})
	var down atomic.Bool
	pool := flakyPool(t, e, &down)
	metrics := observability.NewMetrics()
	w := NewStateWriter(pool, metrics, nil)
	w.retryBackoff = 10 * time.Millisecond
	w.batchSize = 2

	down.Store(true)
	pool.Reset()
	now := time.Now().UTC().Truncate(time.Microsecond)
	w.Enqueue(identityChange("idt_1", now, StateBanned, OpBan))
	for i := 0; i < 4; i++ {
		c := identityChange("idt_1", now.Add(-time.Minute), StateActive, OpCooldown)
		c.UpdateSubject = false
		w.Enqueue(c)
	}
	for i := 0; i < 3; i++ {
		require.Error(t, w.Flush(e.ctx))
	}
	require.Zero(t, w.Dropped(), "nothing is dropped while PostgreSQL is down")
	require.Equal(t, 5, w.Pending())
	require.Equal(t, 3.0, counterValue(metrics.DBWriteBatches.WithLabelValues(stateWriterName, "error")),
		"an unavailable database is not retried within one flush")

	// Run backs off while PostgreSQL is down and writes everything after recovery.
	w.flushEvery = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	time.Sleep(300 * time.Millisecond)
	require.Zero(t, w.Dropped())
	require.Equal(t, 5, w.Pending())
	down.Store(false)
	require.Eventually(t, func() bool { return w.Pending() == 0 }, 10*time.Second, 20*time.Millisecond)
	cancel()
	require.NoError(t, <-done)
	require.Zero(t, w.Dropped())
	require.EqualValues(t, 5, w.Written())
	state, _, _, _ := e.identityState("idt_1")
	require.Equal(t, StateBanned, state)
	require.Len(t, e.stateEvents("idt_1"), 5)
}

func TestStateWriterDropsOnlyRejectedChanges(t *testing.T) {
	e := newEnv(t, true)
	for i := 1; i <= 5; i++ {
		e.addIdentity(identitySeed{ID: fmt.Sprintf("idt_%d", i), Key: int64(100 + i), NoRedis: true})
	}
	metrics := observability.NewMetrics()
	w := NewStateWriter(e.pool, metrics, nil)
	w.retryBackoff = time.Millisecond
	now := time.Now().UTC().Truncate(time.Microsecond)
	for i := 1; i <= 5; i++ {
		c := identityChange(fmt.Sprintf("idt_%d", i), now, StateBanned, OpBan)
		if i == 4 {
			c.Reason = "bad \xff utf-8" // rejected by PostgreSQL (SQLSTATE 22021)
		}
		w.Enqueue(c)
	}
	require.Error(t, w.Flush(e.ctx))
	require.EqualValues(t, 1, w.Dropped())
	require.EqualValues(t, 4, w.Written())
	require.Zero(t, w.Pending())
	for i := 1; i <= 5; i++ {
		id := fmt.Sprintf("idt_%d", i)
		state, _, _, _ := e.identityState(id)
		if i == 4 {
			require.Equal(t, StateActive, state)
			require.Empty(t, e.stateEvents(id))
			continue
		}
		require.Equal(t, StateBanned, state, id)
		require.Len(t, e.stateEvents(id), 1, id)
	}
}

func TestStateWriterStoresEventsOnce(t *testing.T) {
	e := newEnv(t, true)
	e.addIdentity(identitySeed{ID: "idt_1", Key: 101, NoRedis: true})
	w := NewStateWriter(e.pool, nil, nil)
	c := identityChange("idt_1", time.Now().UTC().Truncate(time.Microsecond), StateBanned, OpBan)
	c.ID = "evt_0123456789abcdef0123456789abcdef"
	w.Enqueue(c)
	require.NoError(t, w.Flush(e.ctx))
	// The same change again (a retried batch or a replayed report).
	w.Enqueue(c)
	require.NoError(t, w.Flush(e.ctx))
	require.Len(t, e.stateEvents("idt_1"), 1)
}
