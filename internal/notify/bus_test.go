package notify

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/catalog/catalogtest"
	"github.com/Evil0ctal/Spinneret/internal/events"
	"github.com/Evil0ctal/Spinneret/internal/pkg/idgen"
)

const webCookieType = `
name: web_cookie
site: shop
client: web
fields:
  sessionid: { type: string, required: true, sensitive: true }
`

func publish(t *testing.T, e *env, ev events.Event) {
	t.Helper()
	require.NoError(t, e.bus.Publish(context.Background(), events.NamespaceChannel(e.ns.ID), ev))
}

func eventData(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

func waitAlerts(t *testing.T, e *env, kind string, n int) []AlertEvent {
	t.Helper()
	var got []AlertEvent
	require.Eventually(t, func() bool {
		page, err := e.svc.ListAlertEvents(context.Background(), AlertQuery{
			TenantID: e.tenantID, IncludeTenant: true, NamespaceIDs: []string{e.ns.ID}, Kind: kind,
		})
		require.NoError(t, err)
		got = page.Events
		return len(got) >= n
	}, 5*time.Second, 10*time.Millisecond)
	return got
}

func TestBusBreakerTransitions(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{})
	hook := newProvider(t)
	e.webhookChannel("breakers", e.ns.ID, hook.URL, []string{KindBreakerOpened, KindBreakerReopened, KindBreakerClosed}, nil, SeverityInfo)
	group := catalogtest.AddGroup(e.site, "eg_search", "web", "search", 77)
	e.startService()

	at := time.Now().UTC().Truncate(time.Millisecond)
	transition := func(from, to string, version int64, extra map[string]any) events.Event {
		data := map[string]any{
			"site_id": e.site.ID, "endpoint_group_id": group.ID, "from": from, "to": to,
			"trigger": "auto", "reason": "risk ratio 0.52 >= 0.40", "version": version,
		}
		for k, v := range extra {
			data[k] = v
		}
		return events.Event{Type: events.TypeBreakerTransition, TenantID: e.tenantID, NamespaceID: e.ns.ID,
			SiteID: e.site.ID, At: at, Data: eventData(t, data)}
	}

	publish(t, e, transition("closed", "open", 1, map[string]any{"open_until": at.Add(2 * time.Minute).Format(time.RFC3339Nano)}))
	opened := waitAlerts(t, e, KindBreakerOpened, 1)[0]
	require.Equal(t, SeverityCritical, opened.Severity)
	require.Equal(t, "Breaker opened: shop/web/search", opened.Title)
	require.Equal(t, e.site.ID, opened.SiteID)
	require.Equal(t, "search", opened.Details["endpoint_group"])
	require.Equal(t, at.Add(2*time.Minute).Format(time.RFC3339), opened.Details["open_until"])
	require.Contains(t, opened.Message, "closed → open")

	// The same transition delivered twice (e.g. by two instances) alerts once.
	publish(t, e, transition("closed", "open", 1, nil))
	// open → half_open is not alerted; half_open → open reopens; → closed closes.
	publish(t, e, transition("open", "half_open", 2, nil))
	publish(t, e, transition("half_open", "open", 3, map[string]any{"open_until": at.Add(4 * time.Minute).UnixMilli()}))
	reopened := waitAlerts(t, e, KindBreakerReopened, 1)[0]
	require.Equal(t, SeverityCritical, reopened.Severity)
	publish(t, e, events.Event{Type: events.TypeBreakerTransition, NamespaceID: e.ns.ID, At: at,
		Data: eventData(t, map[string]any{"site_id": e.site.ID, "endpoint_group_id": group.ID, "from_state": "half_open", "to_state": "closed", "v": 4})})
	closed := waitAlerts(t, e, KindBreakerClosed, 1)[0]
	require.Equal(t, SeverityInfo, closed.Severity)

	// Events without a known namespace or site, alerts and other types are ignored.
	publish(t, e, events.Event{Type: events.TypeBreakerTransition, NamespaceID: "ns_unknown", At: at,
		Data: eventData(t, map[string]any{"site_id": e.site.ID, "from": "closed", "to": "open", "version": 9})})
	publish(t, e, events.Event{Type: events.TypeBreakerTransition, NamespaceID: e.ns.ID, At: at,
		Data: eventData(t, map[string]any{"from": "closed", "to": "open", "version": 10})})
	publish(t, e, events.Event{Type: events.TypeBreakerTransition, NamespaceID: e.ns.ID, At: at, Data: json.RawMessage(`[1]`)})
	publish(t, e, events.Event{Type: events.TypeConfigPublished, NamespaceID: e.ns.ID, At: at, Data: json.RawMessage(`{}`)})
	require.NoError(t, e.bus.Publish(context.Background(), events.ChannelCatalog, events.Event{Type: events.TypeBreakerTransition}))

	// Deliveries: opened + reopened + closed.
	require.Eventually(t, func() bool { return len(hook.received()) == 3 }, 5*time.Second, 10*time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	require.Len(t, waitAlerts(t, e, KindBreakerOpened, 1), 1)
	require.Len(t, e.alerts(), 3)
}

func TestBusIdentityExpired(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{})
	hook := newProvider(t)
	e.webhookChannel("refresh", e.ns.ID, hook.URL, []string{KindIdentityExpired}, nil, SeverityInfo)

	typeID := idgen.New(idgen.IdentityType)
	e.exec(`INSERT INTO identity_types (id, site_id, client, name) VALUES ($1, $2, 'web', 'web_cookie')`, typeID, e.site.ID)
	catalogtest.AddIdentityType(e.site, catalogtest.MustCompileType(typeID, e.site.ID, 1, webCookieType))
	identityID := idgen.New(idgen.Identity)
	e.exec(`INSERT INTO identities (id, site_id, client, type_id, unique_hash, payload_hash) VALUES ($1, $2, 'web', $3, '\x01', '\x02')`,
		identityID, e.site.ID, typeID)
	e.startService()

	at := time.Now().UTC()
	change := func(subjectKind, subjectID, from, to string) events.Event {
		return events.Event{Type: events.TypeIdentityState, TenantID: e.tenantID, NamespaceID: e.ns.ID, SiteID: e.site.ID, At: at,
			Data: eventData(t, map[string]any{
				"subject_kind": subjectKind, "subject_id": subjectID, "site_id": e.site.ID, "from": from, "to": to,
				"action": "expire", "until": nil, "reason": "auth_invalid x3",
			})}
	}
	publish(t, e, change("identity", identityID, "active", "banned"))
	publish(t, e, change("proxy", "pxy_1", "active", "expired"))
	publish(t, e, change("identity", identityID, "active", "expired"))

	got := waitAlerts(t, e, KindIdentityExpired, 1)
	require.Len(t, got, 1)
	a := got[0]
	require.Equal(t, SeverityInfo, a.Severity)
	require.Equal(t, map[string]any{
		"identity_id": identityID, "site": "shop", "site_id": e.site.ID, "type": "web_cookie",
		"client": "web", "reason": "auth_invalid x3", "from": "active",
	}, a.Details)
	require.Eventually(t, func() bool { return len(hook.received()) == 1 }, 5*time.Second, 10*time.Millisecond)
	body := decodeJSON(t, hook.received()[0].Body)
	require.Equal(t, KindIdentityExpired, body["kind"])
	require.Equal(t, identityID, body["details"].(map[string]any)["identity_id"])

	// A deleted identity still alerts without type information.
	deleted := idgen.New(idgen.Identity)
	publish(t, e, change("", deleted, "active", "expired"))
	require.Eventually(t, func() bool {
		return len(waitAlerts(t, e, KindIdentityExpired, 1)) == 2
	}, 5*time.Second, 10*time.Millisecond)
	for _, ev := range e.alerts() {
		if ev.Details["identity_id"] == deleted {
			require.Equal(t, "", ev.Details["type"])
		}
	}
}

func TestBusQueueFullAndRunTwice(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{})
	// Without Run nothing consumes the bus queue; overflowing events are dropped.
	unsubscribe := e.bus.Subscribe(events.ChannelAll, e.svc.onBusEvent)
	defer unsubscribe()
	expired := json.RawMessage(`{"subject_kind":"identity","subject_id":"idt_1","from":"active","to":"expired"}`)
	for range defaultBusQueueSize + 5 {
		publish(t, e, events.Event{Type: events.TypeIdentityState, Data: expired})
	}
	require.EqualValues(t, 5, e.svc.droppedBusEvents.Load())

	e.startService()
	require.ErrorIs(t, e.svc.Run(context.Background()), ErrRunning)
}

func TestFlexTime(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		in   string
		want time.Time
	}{
		{`"2026-09-17T10:00:00Z"`, at},
		{strconv.FormatInt(at.UnixMilli(), 10), at},
		{`1.7581032e12`, time.UnixMilli(1758103200000)},
		{`null`, time.Time{}},
		{`"not a time"`, time.Time{}},
		{`-5`, time.Time{}},
		{`true`, time.Time{}},
	}
	for _, tt := range tests {
		var ft flexTime
		require.NoError(t, json.Unmarshal([]byte(tt.in), &ft), tt.in)
		require.True(t, tt.want.Equal(ft.Time), tt.in)
	}
	var d breakerTransition
	require.NoError(t, json.Unmarshal([]byte(`{"from":"closed","to":"open","open_until":"garbage"}`), &d))
	require.Equal(t, "open", d.To)
}

// The breaker track publishes TransitionData without a version; site switches
// reuse the event type with running/paused states.
func TestBusBreakerTrackEventShape(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{})
	group := catalogtest.AddGroup(e.site, "eg_feed", "web", "feed", 88)
	e.startService()

	at := time.Now().UTC().Truncate(time.Millisecond)
	openUntil := at.Add(2 * time.Minute)
	transition := json.RawMessage(`{"namespace":"prod","site":"shop","site_id":"` + e.site.ID + `","client":"web",` +
		`"endpoint_group":"feed","endpoint_group_id":"` + group.ID + `","from":"closed","to":"open","trigger":"auto",` +
		`"reason":"risk ratio 0.45 >= 0.40","open_until":"` + openUntil.Format(time.RFC3339Nano) + `","consecutive_opens":1,` +
		`"manual":false,"actor":"system","metrics":{"total":120,"success":40,"risk":54,"captcha_identities":2,` +
		`"risk_ratio":0.45,"success_ratio":0.3333,"probe_samples":0,"probe_successes":0}}`)
	ev := events.Event{Type: events.TypeBreakerTransition, TenantID: e.tenantID, NamespaceID: e.ns.ID, SiteID: e.site.ID, At: at, Data: transition}
	publish(t, e, ev)
	publish(t, e, ev) // delivered again by a peer instance
	siteSwitch := json.RawMessage(`{"namespace":"prod","site":"shop","site_id":"` + e.site.ID + `","client":"",` +
		`"endpoint_group":"","endpoint_group_id":"","from":"running","to":"paused","trigger":"site_switch","reason":"maintenance",` +
		`"open_until":null,"consecutive_opens":0,"manual":true,"actor":"user:usr_1","metrics":{}}`)
	publish(t, e, events.Event{Type: events.TypeBreakerTransition, TenantID: e.tenantID, NamespaceID: e.ns.ID, SiteID: e.site.ID, At: at, Data: siteSwitch})

	opened := waitAlerts(t, e, KindBreakerOpened, 1)
	a := opened[0]
	require.Equal(t, "Breaker opened: shop/web/feed", a.Title)
	require.Equal(t, openUntil.Format(time.RFC3339), a.Details["open_until"])
	require.EqualValues(t, 120, a.Details["metrics"].(map[string]any)["total"])
	require.Equal(t, "breaker:"+group.ID+":closed>open:t"+strconv.FormatInt(at.UnixMilli(), 10), a.DedupKey)
	time.Sleep(100 * time.Millisecond)
	require.Len(t, e.alerts(), 1, "duplicates and site switches do not alert")
}
