package action

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/observability"
)

func TestStateWriterQueueFullAndRun(t *testing.T) {
	e := newEnv(t, true)
	w := NewStateWriter(e.pool, observability.NewMetrics(), nil)
	w.capacity = 3
	w.batchSize = 2
	w.flushEvery = 20 * time.Millisecond
	for i := 0; i < 3; i++ {
		w.Enqueue(identityChange(fmt.Sprintf("idt_%d", i), time.Now(), StateBanned, OpBan))
	}
	require.Zero(t, w.Dropped())

	// A full queue of lifecycle changes applies backpressure: the enqueue
	// waits and gives up when its context ends.
	ctx, cancel := context.WithTimeout(e.ctx, 50*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, w.EnqueueContext(ctx, identityChange("idt_late", time.Now(), StateBanned, OpBan)), context.DeadlineExceeded)
	require.EqualValues(t, 1, w.Dropped())

	runCtx, stop := context.WithCancel(e.ctx)
	done := make(chan error, 1)
	go func() { done <- w.Run(runCtx) }()
	// A waiting lifecycle change is queued as soon as a flush frees space.
	require.NoError(t, w.EnqueueContext(e.ctx, identityChange("idt_waiting", time.Now(), StateBanned, OpBan)))
	require.Eventually(t, func() bool { return w.Written() == 4 }, 5*time.Second, 10*time.Millisecond)

	// Changes queued right before shutdown are drained by the final flush.
	w.Enqueue(identityChange("idt_last", time.Now(), StateBanned, OpBan))
	stop()
	require.NoError(t, <-done)
	require.Len(t, e.stateEvents("idt_last"), 1)
	require.Len(t, e.stateEvents("idt_waiting"), 1)
}

func TestStateWriterSpillsOldestNonLifecycleChanges(t *testing.T) {
	e := newEnv(t, true)
	e.addIdentity(identitySeed{ID: "idt_1", Key: 101, NoRedis: true})
	metrics := observability.NewMetrics()
	w := NewStateWriter(e.pool, metrics, nil)
	w.capacity = 3
	now := time.Now().UTC().Truncate(time.Microsecond)
	cooldown := func(reason string) StateChange {
		c := identityChange("idt_1", now, StateActive, OpCooldown)
		c.UpdateSubject, c.Reason = false, reason
		return c
	}
	w.Enqueue(cooldown("oldest"))
	w.Enqueue(identityChange("idt_1", now, StateBanned, OpBan))
	w.Enqueue(cooldown("newer"))
	// Full: the oldest cooldown event makes room for the lifecycle change.
	require.NoError(t, w.EnqueueContext(e.ctx, identityChange("idt_1", now.Add(time.Second), StateQuarantined, OpQuarantine)))
	// Full again: a new cooldown event spills the remaining older one.
	w.Enqueue(cooldown("newest"))
	require.EqualValues(t, 2, w.Spilled())
	require.EqualValues(t, 2, w.Dropped())
	require.Equal(t, 3, w.Pending())

	require.NoError(t, w.Flush(e.ctx))
	var reasons []string
	for _, ev := range e.stateEvents("idt_1") {
		reasons = append(reasons, ev.Action+":"+ev.Reason)
	}
	require.ElementsMatch(t, []string{"ban:", "quarantine:", "cooldown:newest"}, reasons)

	families, err := metrics.Registry.Gather()
	require.NoError(t, err)
	found := map[string]float64{}
	for _, mf := range families {
		for _, m := range mf.GetMetric() {
			switch {
			case m.GetCounter() != nil:
				found[mf.GetName()] = m.GetCounter().GetValue()
			case m.GetGauge() != nil:
				found[mf.GetName()] = m.GetGauge().GetValue()
			}
		}
	}
	require.Equal(t, 2.0, found["spinneret_state_writer_spilled_changes_total"])
	require.Equal(t, 2.0, found["spinneret_state_writer_dropped_changes_total"])
	require.Contains(t, found, "spinneret_state_writer_pending_changes")
}

func TestStateWriterErrorClasses(t *testing.T) {
	tests := []struct {
		name              string
		err               error
		unavailable, data bool
	}{
		{name: "connection refused", err: &net.OpError{Op: "dial", Err: errors.New("refused")}, unavailable: true},
		{name: "write timeout", err: fmt.Errorf("commit: %w", context.DeadlineExceeded), unavailable: true},
		{name: "admin shutdown", err: &pgconn.PgError{Code: "57P01"}, unavailable: true},
		{name: "too many connections", err: &pgconn.PgError{Code: "53300"}, unavailable: true},
		{name: "statement canceled", err: &pgconn.PgError{Code: "57014"}},
		{name: "invalid text", err: &pgconn.PgError{Code: "22021"}, data: true},
		{name: "missing partition", err: &pgconn.PgError{Code: "23514", Message: "no partition of relation"}, data: true},
		{name: "undefined table", err: &pgconn.PgError{Code: "42P01"}},
		{name: "encoding bug", err: errors.New("unable to encode value")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.unavailable, isStateDBUnavailable(tc.err))
			require.Equal(t, tc.data, isStateDataError(tc.err))
		})
	}
	require.True(t, isMissingPartition(&pgconn.PgError{Code: "23514", Message: "no partition of relation \"state_events\" found for row"}))
	require.False(t, isMissingPartition(&pgconn.PgError{Code: "23514", ConstraintName: "x", Message: "check"}))
}
