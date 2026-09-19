package notify

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/catalog/catalogtest"
	"github.com/TikHub/Spinneret/internal/events"
	"github.com/TikHub/Spinneret/internal/pkg/idgen"
)

// expiryFixture seeds identities of one identity type.
type expiryFixture struct {
	e      *env
	typeID string
}

func newExpiryFixture(e *env) *expiryFixture {
	typeID := idgen.New(idgen.IdentityType)
	e.exec(`INSERT INTO identity_types (id, site_id, client, name) VALUES ($1, $2, 'web', 'web_cookie')`, typeID, e.site.ID)
	catalogtest.AddIdentityType(e.site, catalogtest.MustCompileType(typeID, e.site.ID, 1, webCookieType))
	return &expiryFixture{e: e, typeID: typeID}
}

func (f *expiryFixture) identity() string {
	id := idgen.New(idgen.Identity)
	f.e.exec(`INSERT INTO identities (id, site_id, client, type_id, unique_hash, payload_hash) VALUES ($1, $2, 'web', $3, $4, '\x02')`,
		id, f.e.site.ID, f.typeID, []byte(id))
	return id
}

// stateEvent inserts a state_events row as the StateWriter persists it.
func (f *expiryFixture) stateEvent(identityID string, at time.Time, from, to, action string, shadow bool) {
	f.e.exec(`INSERT INTO state_events (id, created_at, tenant_id, namespace_id, site_id, subject_kind, subject_id,
			from_state, to_state, action, reason, actor, shadow)
		VALUES ($1, $2, $3, $4, $5, 'identity', $6, $7, $8, $9, 'auth_invalid x3', 'system', $10)`,
		idgen.New(idgen.StateEvent), at, f.e.tenantID, f.e.ns.ID, f.e.site.ID, identityID, from, to, action, shadow)
}

// expiredEvent is the identity.state event published with the change.
func (f *expiryFixture) expiredEvent(t *testing.T, identityID string, at time.Time) events.Event {
	return events.Event{Type: events.TypeIdentityState, TenantID: f.e.tenantID, NamespaceID: f.e.ns.ID, SiteID: f.e.site.ID, At: at,
		Data: eventData(t, map[string]any{
			"subject_kind": "identity", "subject_id": identityID, "site_id": f.e.site.ID, "from": "active", "to": "expired",
			"action": "expire", "until": nil, "reason": "auth_invalid x3",
		})}
}

func expiredIdentityIDs(e *env) map[string]int {
	out := map[string]int{}
	for _, a := range e.alerts() {
		if a.Kind == KindIdentityExpired {
			id, _ := a.Details["identity_id"].(string)
			out[id]++
		}
	}
	return out
}

// TestIdentityExpiredRecoveredAfterBusOverflow is the regression test for
// identity_expired webhooks lost when a burst of identity.state events
// overflowed the notify bus queue: the alert evaluation job recovers them
// from state_events, without duplicating the alerts the bus did deliver.
func TestIdentityExpiredRecoveredAfterBusOverflow(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{})
	hook := newProvider(t)
	e.webhookChannel("refresh", e.ns.ID, hook.URL, []string{KindIdentityExpired}, nil, SeverityInfo)
	f := newExpiryFixture(e)
	ctx := context.Background()

	const queued, total = 2, 6
	e.svc.busQueue = make(chan busItem, queued)
	unsubscribe := e.bus.Subscribe(events.ChannelAll, e.svc.onBusEvent)
	at := time.Now().UTC().Add(-time.Second).Truncate(time.Microsecond)
	ids := make([]string, 0, total)
	for range total {
		id := f.identity()
		ids = append(ids, id)
		f.stateEvent(id, at, "active", "expired", "expire", false)
		publish(t, e, f.expiredEvent(t, id, at))
	}
	unsubscribe()
	require.EqualValues(t, total-queued, e.svc.droppedBusEvents.Load())

	// The queued events are converted by the bus loop.
	e.startService()
	require.Eventually(t, func() bool { return len(expiredIdentityIDs(e)) == queued }, alertWait, 10*time.Millisecond)

	// The evaluation job restores the dropped ones.
	require.NoError(t, e.svc.EvaluateJob().Run(ctx))
	got := expiredIdentityIDs(e)
	require.Len(t, got, total)
	for _, id := range ids {
		require.Equal(t, 1, got[id], "identity %s must alert exactly once", id)
	}
	require.Eventually(t, func() bool { return len(hook.received()) == total }, alertWait, 10*time.Millisecond)
	for _, a := range e.alerts() {
		require.Equal(t, "web_cookie", a.Details["type"])
		require.Equal(t, "shop", a.Details["site"])
		require.Equal(t, "auth_invalid x3", a.Details["reason"])
	}

	// Later runs do not alert again.
	require.NoError(t, e.svc.EvaluateJob().Run(ctx))
	require.Len(t, e.alerts(), total)
}

// TestIdentityExpiredRecoveryWatermark covers rows persisted late (inside the
// settle window), rows that are not enforced expirations, and the watermark
// that keeps rows from being alerted again once their de-duplication marker
// has expired.
func TestIdentityExpiredRecoveryWatermark(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{})
	f := newExpiryFixture(e)
	ctx := context.Background()
	start := time.Now().UTC()
	clock := start
	e.svc.now = func() time.Time { return clock }

	// Rows before the first run are history and are not alerted.
	f.stateEvent(f.identity(), start.Add(-expiredSettleWindow-time.Minute), "active", "expired", "expire", false)
	require.NoError(t, e.svc.recoverExpiredIdentities(ctx))
	require.Empty(t, e.alerts())

	// Not enforced expirations are ignored.
	ignored := f.identity()
	f.stateEvent(ignored, start.Add(-time.Second), "active", "banned", "ban", false)
	f.stateEvent(ignored, start.Add(-time.Second), "active", "expired", "expire", true)
	f.stateEvent(ignored, start.Add(-time.Second), "expired", "expired", "expire", false)

	// A row persisted late, with an event time inside the settle window.
	late := f.identity()
	f.stateEvent(late, start.Add(-30*time.Second), "active", "expired", "expire", false)
	clock = start.Add(10 * time.Second)
	require.NoError(t, e.svc.recoverExpiredIdentities(ctx))
	require.Equal(t, map[string]int{late: 1}, expiredIdentityIDs(e))

	// Once the settle window passed, the row is not visited again even if
	// its de-duplication marker is gone.
	clock = start.Add(expiredSettleWindow + time.Minute)
	require.NoError(t, e.svc.recoverExpiredIdentities(ctx))
	e.deleteAlertDedupKeys(ctx)
	clock = clock.Add(10 * time.Second)
	require.NoError(t, e.svc.recoverExpiredIdentities(ctx))
	require.Equal(t, map[string]int{late: 1}, expiredIdentityIDs(e))

	// The watermark survives the service (it is stored in PostgreSQL).
	fresh := New(Config{}, e.pool, nil, e.rdb, e.keys, e.cat, nil, nil, nil, nil)
	fresh.now = func() time.Time { return clock }
	next := f.identity()
	f.stateEvent(next, clock.Add(-time.Second), "pending", "expired", "expire", false)
	require.NoError(t, fresh.recoverExpiredIdentities(ctx))
	require.Equal(t, map[string]int{late: 1, next: 1}, expiredIdentityIDs(e))
}

// TestIdentityExpiredRecoveryBudget checks that a run stopping at its time
// budget resumes where it stopped.
func TestIdentityExpiredRecoveryBudget(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{})
	f := newExpiryFixture(e)
	ctx := context.Background()
	require.NoError(t, e.svc.recoverExpiredIdentities(ctx)) // establishes the watermark

	at := time.Now().UTC().Truncate(time.Microsecond)
	const n = 7
	for range n {
		f.stateEvent(f.identity(), at, "active", "expired", "expire", false)
	}
	calls := 0
	e.svc.expiredRecoveryBudget = func() bool { // allow three alerts per run
		calls++
		return calls%4 != 0
	}
	for range 3 {
		require.NoError(t, e.svc.recoverExpiredIdentities(ctx))
	}
	require.Len(t, expiredIdentityIDs(e), n)
}

// TestIdentityExpiredRecoverySkipsUnemittableRows checks that a transition
// whose alert can never be stored (here: details above MaxDetailsBytes) is
// skipped instead of stopping every later run at the same row, which would
// keep the transitions recorded after it from ever being recovered.
func TestIdentityExpiredRecoverySkipsUnemittableRows(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{})
	f := newExpiryFixture(e)
	ctx := context.Background()
	require.NoError(t, e.svc.recoverExpiredIdentities(ctx)) // establishes the watermark

	at := time.Now().UTC().Truncate(time.Microsecond)
	poisoned := f.identity()
	f.stateEvent(poisoned, at, "active", "expired", "expire", false)
	e.exec(`UPDATE state_events SET reason = $1 WHERE subject_id = $2`, strings.Repeat("x", MaxDetailsBytes+1), poisoned)
	next := f.identity()
	f.stateEvent(next, at.Add(time.Millisecond), "active", "expired", "expire", false)

	for range 2 {
		require.NoError(t, e.svc.recoverExpiredIdentities(ctx))
		require.Equal(t, map[string]int{next: 1}, expiredIdentityIDs(e))
	}
}

func (e *env) deleteAlertDedupKeys(ctx context.Context) {
	e.t.Helper()
	keys, err := e.rdb.Do(ctx, e.rdb.B().Keys().Pattern(e.keys.AlertDedup("*")).Build()).AsStrSlice()
	require.NoError(e.t, err)
	if len(keys) > 0 {
		require.NoError(e.t, e.rdb.Do(ctx, e.rdb.B().Del().Key(keys...).Build()).Error())
	}
}

// TestBusQueueOnlyHoldsAlertingEvents checks that identity.state events that
// cannot alert do not take bus queue slots.
func TestBusQueueOnlyHoldsAlertingEvents(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{})
	f := newExpiryFixture(e)
	unsubscribe := e.bus.Subscribe(events.ChannelAll, e.svc.onBusEvent)
	defer unsubscribe()
	at := time.Now().UTC()
	banned := f.expiredEvent(t, "idt_1", at)
	banned.Data = eventData(t, map[string]any{"subject_kind": "identity", "subject_id": "idt_1", "from": "active", "to": "banned"})
	publish(t, e, banned)
	proxy := f.expiredEvent(t, "pxy_1", at)
	proxy.Data = eventData(t, map[string]any{"subject_kind": "proxy", "subject_id": "pxy_1", "from": "active", "to": "expired"})
	publish(t, e, proxy)
	require.Empty(t, e.svc.busQueue)
	publish(t, e, f.expiredEvent(t, "idt_2", at))
	require.Len(t, e.svc.busQueue, 1)
}
