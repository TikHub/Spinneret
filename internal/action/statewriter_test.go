package action

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/observability"
)

func identityChange(id string, at time.Time, to, action string) StateChange {
	return StateChange{
		At: at, TenantID: testTenant, NamespaceID: testNamespace, SiteID: siteAID,
		SubjectKind: SubjectIdentity, SubjectID: id, FromState: StateActive, ToState: to,
		Action: action, Scope: "identity", Rule: "rule-" + action, UpdateSubject: true,
	}
}

func TestStateWriterBatchesAndUpdatesSubjects(t *testing.T) {
	e := newEnv(t, true)
	metrics := observability.NewMetrics()
	w := NewStateWriter(e.pool, metrics, nil)
	e.addIdentity(identitySeed{ID: "idt_1", Key: 101, NoRedis: true})
	e.addIdentity(identitySeed{ID: "idt_2", Key: 102, NoRedis: true, State: StatePending})
	e.addAccount("acc_1", 7, e.siteA, StateActive)
	e.addProxy("pxy_1", 55, StateActive)
	now := time.Now().UTC().Truncate(time.Microsecond)

	for i := 0; i < 1100; i++ {
		c := identityChange("idt_1", now.Add(-time.Minute), StateActive, OpCooldown)
		c.UpdateSubject = false
		w.Enqueue(c)
	}
	banUntil := now.Add(12 * time.Hour)
	ban := identityChange("idt_1", now, StateBanned, OpBan)
	ban.Until = &banUntil
	w.Enqueue(ban)
	w.Enqueue(identityChange("idt_2", now, StateActive, OpActivate))
	// Account resolved from its hkey; proxy quarantine stores its end in ban_until.
	w.Enqueue(StateChange{At: now, TenantID: testTenant, NamespaceID: testNamespace, SiteID: siteAID,
		SubjectKind: SubjectAccount, SubjectKey: 7, FromState: StateActive, ToState: StateBanned, Action: OpBan,
		Permanent: true, UpdateSubject: true})
	w.Enqueue(StateChange{At: now, TenantID: testTenant, NamespaceID: testNamespace,
		SubjectKind: SubjectProxy, SubjectID: "pxy_1", FromState: StateActive, ToState: StateQuarantined, Action: OpQuarantine,
		Until: &banUntil, UpdateSubject: true, StateReason: "proxy-quarantine"})
	// Unknown subjects and shadow changes never touch rows.
	w.Enqueue(StateChange{At: now, TenantID: testTenant, NamespaceID: testNamespace, SubjectKind: SubjectAccount, SubjectKey: 999, ToState: StateBanned, Action: OpBan})
	shadow := identityChange("idt_2", now.Add(time.Second), StateExpired, OpExpire)
	shadow.Shadow = true
	w.Enqueue(shadow)
	require.Equal(t, 1106, w.Pending())

	require.NoError(t, w.Flush(e.ctx))
	require.Zero(t, w.Pending())
	require.EqualValues(t, 1106, w.Written())
	require.Equal(t, 3.0, counterValue(metrics.DBWriteBatches.WithLabelValues(stateWriterName, "ok")))

	state, reason, bu, _ := e.identityState("idt_1")
	require.Equal(t, StateBanned, state)
	require.Equal(t, "rule-ban", reason)
	require.WithinDuration(t, banUntil, *bu, time.Millisecond)
	state, _, _, _ = e.identityState("idt_2")
	require.Equal(t, StateActive, state)
	var activated *time.Time
	require.NoError(t, e.pool.QueryRow(e.ctx, `SELECT activated_at FROM identities WHERE id = 'idt_2'`).Scan(&activated))
	require.NotNil(t, activated)

	var accState string
	var accBan *time.Time
	require.NoError(t, e.pool.QueryRow(e.ctx, `SELECT state, ban_until FROM accounts WHERE id = 'acc_1'`).Scan(&accState, &accBan))
	require.Equal(t, StateBanned, accState)
	require.Nil(t, accBan)
	var pxState, pxReason string
	var pxUntil *time.Time
	require.NoError(t, e.pool.QueryRow(e.ctx, `SELECT state, state_reason, ban_until FROM proxies WHERE id = 'pxy_1'`).Scan(&pxState, &pxReason, &pxUntil))
	require.Equal(t, StateQuarantined, pxState)
	require.Equal(t, "proxy-quarantine", pxReason)
	require.NotNil(t, pxUntil)

	require.Len(t, e.stateEvents("idt_1"), 1101)
	accEvents := e.stateEvents("acc_1")
	require.Len(t, accEvents, 1)
	require.True(t, accEvents[0].Permanent)
	require.Equal(t, ActorSystem, accEvents[0].Actor)
	idt2 := e.stateEvents("idt_2")
	require.Len(t, idt2, 2)
	require.True(t, idt2[1].Shadow)
}

func TestStateWriterOrdering(t *testing.T) {
	e := newEnv(t, true)
	w := NewStateWriter(e.pool, nil, nil)
	now := time.Now().UTC().Truncate(time.Microsecond)
	e.addIdentity(identitySeed{ID: "idt_1", Key: 101, NoRedis: true})
	e.addIdentity(identitySeed{ID: "idt_2", Key: 102, NoRedis: true})
	e.addIdentity(identitySeed{ID: "idt_3", Key: 103, NoRedis: true, State: StateDisabled, ChangedAt: now.Add(time.Hour)})

	// Within one batch the latest change wins, ties go to the last enqueued.
	w.Enqueue(identityChange("idt_1", now.Add(time.Second), StateQuarantined, OpQuarantine))
	w.Enqueue(identityChange("idt_1", now, StateExpired, OpExpire))
	w.Enqueue(identityChange("idt_2", now, StateExpired, OpExpire))
	w.Enqueue(identityChange("idt_2", now, StateBanned, OpBan))
	// A row changed after the automatic change is never overridden.
	w.Enqueue(identityChange("idt_3", now, StateBanned, OpBan))
	require.NoError(t, w.Flush(e.ctx))

	s1, _, _, _ := e.identityState("idt_1")
	s2, _, _, _ := e.identityState("idt_2")
	s3, _, _, _ := e.identityState("idt_3")
	require.Equal(t, []string{StateQuarantined, StateBanned, StateDisabled}, []string{s1, s2, s3})

	// Later batches with older changes do not override either.
	w.Enqueue(identityChange("idt_1", now.Add(-time.Minute), StateActive, OpActivate))
	require.NoError(t, w.Flush(e.ctx))
	s1, _, _, _ = e.identityState("idt_1")
	require.Equal(t, StateQuarantined, s1)
	events := e.stateEvents("idt_2")
	require.Len(t, events, 2)
	require.Equal(t, []string{OpExpire, OpBan}, []string{events[0].Action, events[1].Action})
}

func TestStateWriterRetryAndDrop(t *testing.T) {
	e := newEnv(t, true)
	metrics := observability.NewMetrics()
	w := NewStateWriter(e.pool, metrics, nil)
	w.retryBackoff = 50 * time.Millisecond
	w.maxAttempts = 3

	// No partition exists for this month: every attempt fails, then the
	// single rejected change is isolated and dropped.
	old := time.Date(2001, 3, 1, 0, 0, 0, 0, time.UTC)
	c := identityChange("idt_x", old, StateBanned, OpBan)
	c.UpdateSubject = false
	w.Enqueue(c)
	err := w.Flush(e.ctx)
	require.Error(t, err)
	require.EqualValues(t, 1, w.Dropped())
	require.Equal(t, 4.0, counterValue(metrics.DBWriteBatches.WithLabelValues(stateWriterName, "error")))

	// The batch succeeds once the partition appears between attempts.
	w.maxAttempts = 50
	w.retryBackoff = 100 * time.Millisecond
	c.ID = ""
	w.Enqueue(c)
	done := make(chan error, 1)
	go func() { done <- w.Flush(e.ctx) }()
	require.Eventually(t, func() bool {
		return counterValue(metrics.DBWriteBatches.WithLabelValues(stateWriterName, "error")) >= 5
	}, 5*time.Second, 10*time.Millisecond)
	e.exec(`SELECT spinneret_ensure_partitions('state_events', 'month', $1, $1)`, old)
	require.NoError(t, <-done)
	require.Len(t, e.stateEvents("idt_x"), 1)

	// Canceled contexts stop retrying.
	w.Enqueue(identityChange("idt_y", time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC), StateBanned, OpBan))
	ctx, cancel := context.WithCancel(e.ctx)
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()
	require.ErrorIs(t, w.Flush(ctx), context.Canceled)
}

func TestLatestUpdates(t *testing.T) {
	now := time.Now()
	a := identityChange("idt_1", now, StateBanned, OpBan)
	b := identityChange("idt_1", now.Add(-time.Second), StateExpired, OpExpire)
	c := identityChange("idt_2", now, StateBanned, OpBan)
	c.Shadow = true
	d := StateChange{SubjectKind: SubjectAccount, SubjectID: "acc_1", At: now, ToState: StateBanned, UpdateSubject: true}
	got := latestUpdates([]StateChange{a, b, c, d})
	require.Len(t, got[SubjectIdentity], 1)
	require.Equal(t, StateBanned, got[SubjectIdentity][0].ToState)
	require.Len(t, got[SubjectAccount], 1)
	require.Empty(t, got[SubjectProxy])

	require.Equal(t, "x", stateReason(StateChange{StateReason: "x", Rule: "r", Reason: "y"}))
	require.Equal(t, "y", stateReason(StateChange{Reason: "y"}))
	require.JSONEq(t, `{}`, string(encodeDetails(nil)))
	require.JSONEq(t, `{"truncated":true}`, string(encodeDetails(map[string]any{"bad": make(chan int)})))
}

// partitionlessChange is a change dated in a month without a state_events
// partition, so its writes fail until the partition is created.
func partitionlessChange(id string, month time.Time) StateChange {
	c := identityChange(id, month, StateBanned, OpBan)
	c.UpdateSubject = false
	return c
}

func TestStateWriterKeepsBatchInterruptedByCancellation(t *testing.T) {
	e := newEnv(t, true)
	metrics := observability.NewMetrics()
	w := NewStateWriter(e.pool, metrics, nil)
	w.retryBackoff = 100 * time.Millisecond
	w.maxAttempts = 50
	month := time.Date(1998, 6, 1, 0, 0, 0, 0, time.UTC)
	w.Enqueue(partitionlessChange("idt_carry", month))

	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan error, 1)
	go func() { done <- w.Flush(ctx) }()
	require.Eventually(t, func() bool {
		return counterValue(metrics.DBWriteBatches.WithLabelValues(stateWriterName, "error")) >= 1
	}, 5*time.Second, 5*time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)

	// The interrupted batch is neither dropped nor lost.
	require.Zero(t, w.Dropped())
	require.Equal(t, 1, w.Pending())
	require.ErrorIs(t, w.Flush(ctx), context.Canceled, "a canceled flush does not start another batch")
	require.Equal(t, 1, w.Pending())

	e.exec(`SELECT spinneret_ensure_partitions('state_events', 'month', $1, $1)`, month)
	w.Enqueue(partitionlessChange("idt_next", month))
	require.NoError(t, w.Flush(e.ctx))
	require.Zero(t, w.Pending())
	require.EqualValues(t, 2, w.Written())
	require.Len(t, e.stateEvents("idt_carry"), 1)
	require.Len(t, e.stateEvents("idt_next"), 1)
}

func TestStateWriterShutdownWritesInterruptedBatch(t *testing.T) {
	e := newEnv(t, true)
	metrics := observability.NewMetrics()
	w := NewStateWriter(e.pool, metrics, nil)
	w.flushEvery = 10 * time.Millisecond
	w.retryBackoff = 200 * time.Millisecond
	w.maxAttempts = 100
	month := time.Date(1997, 6, 1, 0, 0, 0, 0, time.UTC)
	w.Enqueue(partitionlessChange("idt_shutdown", month))

	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	require.Eventually(t, func() bool {
		return counterValue(metrics.DBWriteBatches.WithLabelValues(stateWriterName, "error")) >= 1
	}, 5*time.Second, 5*time.Millisecond)
	// Shut down while the periodic flush waits to retry; the final drain
	// retries the batch, which succeeds once the partition exists.
	cancel()
	e.exec(`SELECT spinneret_ensure_partitions('state_events', 'month', $1, $1)`, month)
	require.NoError(t, <-done)
	require.Zero(t, w.Dropped())
	require.Zero(t, w.Pending())
	require.Len(t, e.stateEvents("idt_shutdown"), 1)
}
