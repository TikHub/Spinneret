package notify

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/events"
)

func siteAlert(e *env, kind, severity, dedup string) Alert {
	return Alert{
		Kind: kind, Severity: severity, TenantID: e.tenantID, NamespaceID: e.ns.ID, SiteID: e.site.ID,
		Title: "Ban spike on shop", Message: "many bans", Details: map[string]any{"bans_5m": 42}, DedupKey: dedup,
	}
}

func TestEmitMatchesChannelsAndPersistsDeliveries(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{})
	ctx := context.Background()
	hook := newProvider(t, providerResponse{status: 200})
	other := newProvider(t, providerResponse{status: 200})

	otherNS := e.seedNamespace(e.tenantID, "staging")
	matching := []Channel{
		e.webhookChannel("tenant-all", "", hook.URL+"/tenant", []string{KindBanSpike}, nil, SeverityInfo),
		e.webhookChannel("ns-site", e.ns.ID, hook.URL+"/site", []string{KindBanSpike, KindTest}, []string{e.site.ID}, SeverityWarning),
	}
	// Channels that must not receive the alert.
	e.webhookChannel("other-ns", otherNS.ID, other.URL, []string{KindBanSpike}, nil, SeverityInfo)
	e.webhookChannel("other-site", e.ns.ID, other.URL, []string{KindBanSpike}, []string{e.site2.ID}, SeverityInfo)
	e.webhookChannel("other-kind", e.ns.ID, other.URL, []string{KindBreakerOpened}, nil, SeverityInfo)
	e.webhookChannel("too-severe", e.ns.ID, other.URL, []string{KindBanSpike}, nil, SeverityCritical)
	disabled := e.webhookChannel("disabled", e.ns.ID, other.URL, []string{KindBanSpike}, nil, SeverityInfo)
	_, err := e.svc.UpdateChannel(ctx, admin, disabled.ID, ChannelUpdate{Name: "disabled", EventTypes: []string{KindBanSpike}, Enabled: false})
	require.NoError(t, err)

	var published []events.Event
	var mu sync.Mutex
	unsubscribe := e.bus.Subscribe(events.NamespaceChannel(e.ns.ID), func(_ context.Context, _ string, ev events.Event) {
		mu.Lock()
		defer mu.Unlock()
		published = append(published, ev)
	})
	defer unsubscribe()

	e.startService()
	id, err := e.svc.emit(ctx, siteAlert(e, KindBanSpike, SeverityWarning, "site:1"))
	require.NoError(t, err)
	require.NotEmpty(t, id)

	deliveries := e.waitDeliveries(id, len(matching))
	require.Len(t, deliveries, len(matching))
	names := map[string]bool{}
	for _, d := range deliveries {
		require.True(t, d.OK, d.Error)
		require.Equal(t, 1, d.Attempts)
		names[d.ChannelName] = true
	}
	require.Equal(t, map[string]bool{"tenant-all": true, "ns-site": true}, names)
	require.Empty(t, other.received())

	reqs := hook.received()
	require.Len(t, reqs, 2)
	var payload message
	require.NoError(t, json.Unmarshal(reqs[0].Body, &payload))
	require.Equal(t, id, payload.ID)
	require.Equal(t, "acme", payload.Tenant)
	require.Equal(t, "prod", payload.Namespace)
	require.Equal(t, "shop", payload.Site)
	require.EqualValues(t, 42, payload.Details["bans_5m"])

	require.Eventually(t, func() bool {
		ch, err := e.svc.GetChannel(ctx, matching[0].ID)
		return err == nil && ch.LastDeliveryStatus == "ok" && ch.LastDeliveryAt != nil
	}, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, 2.0, counterValue(t, e.metrics.NotifyDeliveries.WithLabelValues(ChannelWebhook, "ok")))

	mu.Lock()
	require.Len(t, published, 1)
	require.Equal(t, events.TypeAlert, published[0].Type)
	var data alertEventData
	require.NoError(t, json.Unmarshal(published[0].Data, &data))
	require.Equal(t, id, data.ID)
	require.Equal(t, KindBanSpike, data.Kind)
	mu.Unlock()

	// A duplicate within the TTL is suppressed; another tenant's key is independent.
	dup, err := e.svc.emit(ctx, siteAlert(e, KindBanSpike, SeverityWarning, "site:1"))
	require.NoError(t, err)
	require.Empty(t, dup)
	otherTenant := siteAlert(e, KindBanSpike, SeverityWarning, "site:1")
	otherTenant.TenantID, otherTenant.NamespaceID, otherTenant.SiteID = "ten_other", "", ""
	id2, err := e.svc.emit(ctx, otherTenant)
	require.NoError(t, err)
	require.NotEmpty(t, id2)
	ttl, err := e.rdb.Do(ctx, e.rdb.B().Pttl().Key(e.keys.AlertDedup(e.tenantID+":"+KindBanSpike+":site:1")).Build()).AsInt64()
	require.NoError(t, err)
	require.Greater(t, ttl, int64(9*time.Minute/time.Millisecond))

	// Tenant-level alerts are published on every namespace of the tenant.
	var stagingEvents int
	unsubscribe2 := e.bus.Subscribe(events.NamespaceChannel(otherNS.ID), func(context.Context, string, events.Event) {
		mu.Lock()
		stagingEvents++
		mu.Unlock()
	})
	defer unsubscribe2()
	_, err = e.svc.emit(ctx, Alert{Kind: KindReportBacklog, Severity: SeverityCritical, TenantID: e.tenantID, Title: "backlog"})
	require.NoError(t, err)
	mu.Lock()
	require.Equal(t, 1, stagingEvents)
	mu.Unlock()
}

func TestEmitRetriesAndFailures(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{Attempts: 3, RetryDelay: time.Millisecond})
	ctx := context.Background()
	flaky := newProvider(t, providerResponse{status: 500}, providerResponse{status: 503}, providerResponse{status: 200})
	broken := newProvider(t, providerResponse{status: 500})
	gone := newProvider(t, providerResponse{status: 404})
	e.webhookChannel("flaky", e.ns.ID, flaky.URL, []string{KindBanSpike}, nil, "")
	e.webhookChannel("broken", e.ns.ID, broken.URL, []string{KindBanSpike}, nil, "")
	e.webhookChannel("gone", e.ns.ID, gone.URL, []string{KindBanSpike}, nil, "")
	e.startService()

	id, err := e.svc.emit(ctx, siteAlert(e, KindBanSpike, SeverityWarning, ""))
	require.NoError(t, err)
	byName := map[string]Delivery{}
	for _, d := range e.waitDeliveries(id, 3) {
		byName[d.ChannelName] = d
	}
	require.True(t, byName["flaky"].OK)
	require.Equal(t, 3, byName["flaky"].Attempts)
	require.False(t, byName["broken"].OK)
	require.Equal(t, 3, byName["broken"].Attempts)
	require.Equal(t, "http: unexpected status 500", byName["broken"].Error)
	require.False(t, byName["gone"].OK)
	require.Equal(t, 1, byName["gone"].Attempts, "4xx responses are not retried")
	require.Len(t, flaky.received(), 3)
	require.Len(t, broken.received(), 3)
	require.Len(t, gone.received(), 1)

	require.Eventually(t, func() bool {
		page, err := e.svc.ListChannels(ctx, ChannelQuery{TenantID: e.tenantID, NamespaceIDs: []string{e.ns.ID}})
		require.NoError(t, err)
		statuses := map[string]string{}
		for _, ch := range page.Channels {
			statuses[ch.Name] = ch.LastDeliveryStatus
		}
		return statuses["flaky"] == "ok" && statuses["broken"] == "http: unexpected status 500" &&
			statuses["gone"] == "http: unexpected status 404"
	}, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, 2.0, counterValue(t, e.metrics.NotifyDeliveries.WithLabelValues(ChannelWebhook, "error")))
}

func TestEmitQueueFull(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{QueueSize: 1})
	ctx := context.Background()
	hook := newProvider(t)
	e.webhookChannel("a", e.ns.ID, hook.URL, []string{KindBanSpike}, nil, "")
	e.webhookChannel("b", e.ns.ID, hook.URL, []string{KindBanSpike}, nil, "")

	// Without Run the queue does not drain: the second delivery is dropped.
	id, err := e.svc.emit(ctx, siteAlert(e, KindBanSpike, SeverityWarning, ""))
	require.NoError(t, err)
	deliveries := e.waitDeliveries(id, 1)
	require.False(t, deliveries[0].OK)
	require.Equal(t, queueFullMessage, deliveries[0].Error)
	require.Equal(t, 1.0, counterValue(t, e.metrics.NotifyDeliveries.WithLabelValues(ChannelWebhook, "dropped")))

	// Starting the service drains the queued delivery.
	e.startService()
	require.Len(t, e.waitDeliveries(id, 2), 2)
}

func TestEmitValidation(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{})
	ctx := context.Background()
	valid := siteAlert(e, KindBanSpike, SeverityWarning, "")
	tests := []struct {
		name   string
		mutate func(*Alert)
	}{
		{"no tenant", func(a *Alert) { a.TenantID = "" }},
		{"bad kind", func(a *Alert) { a.Kind = "nope" }},
		{"bad severity", func(a *Alert) { a.Severity = "fatal" }},
		{"site without namespace", func(a *Alert) { a.NamespaceID = "" }},
		{"no title", func(a *Alert) { a.Title = "  " }},
		{"long dedup key", func(a *Alert) { a.DedupKey = string(make([]byte, maxDedupKeyBytes+1)) }},
		{"details not json", func(a *Alert) { a.Details = map[string]any{"f": func() {}} }},
		{"details too large", func(a *Alert) { a.Details = map[string]any{"x": string(make([]byte, MaxDetailsBytes))} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := valid
			tt.mutate(&a)
			require.Equal(t, apperr.ReasonInvalidArgument, apperr.ReasonOf(e.svc.Emit(ctx, a)))
		})
	}

	long := valid
	long.Title = "t\n" + strings.Repeat("a", 2*MaxTitleBytes)
	long.Message = strings.Repeat("m", 2*MaxMessageBytes)
	id, err := e.svc.emit(ctx, long)
	require.NoError(t, err)
	row, err := e.svc.q.NotifyAlertGet(ctx, id)
	require.NoError(t, err)
	require.LessOrEqual(t, len(row.Title), MaxTitleBytes)
	require.NotContains(t, row.Title, "\n")
	require.LessOrEqual(t, len(row.Message), MaxMessageBytes)

	// A failed insert releases the dedup key so the alert can be retried.
	withKey := valid
	withKey.DedupKey = "retry"
	withKey.Details = map[string]any{"x": strings.Repeat("x", MaxDetailsBytes)}
	require.Error(t, e.svc.Emit(ctx, withKey))
	withKey.Details = nil
	id, err = e.svc.emit(ctx, withKey)
	require.NoError(t, err)
	require.NotEmpty(t, id)
}

func TestTestChannel(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{})
	ctx := context.Background()
	ok := newProvider(t, providerResponse{status: 200, body: `{"errcode":0}`})
	bad := newProvider(t, providerResponse{status: 200, body: `{"errcode":40035,"errmsg":"invalid"}`})
	good, err := e.svc.CreateChannel(ctx, admin, ChannelInput{
		TenantID: e.tenantID, NamespaceID: e.ns.ID, Name: "wecom", Kind: ChannelWeCom,
		Config: map[string]any{"webhook_url": ok.URL}, EventTypes: []string{KindBreakerOpened}, Enabled: false,
	})
	require.NoError(t, err)
	failing, err := e.svc.CreateChannel(ctx, admin, ChannelInput{
		TenantID: e.tenantID, Name: "dingtalk", Kind: ChannelDingTalk,
		Config: map[string]any{"webhook_url": bad.URL}, EventTypes: []string{KindBreakerOpened}, Enabled: true,
	})
	require.NoError(t, err)

	d, err := e.svc.TestChannel(ctx, admin, good.ID)
	require.NoError(t, err)
	require.True(t, d.OK, "disabled channels and filters are ignored by tests")
	require.Equal(t, "wecom", d.ChannelName)
	require.Len(t, ok.received(), 1)

	d, err = e.svc.TestChannel(ctx, admin, failing.ID)
	require.NoError(t, err)
	require.False(t, d.OK)
	require.Equal(t, 1, d.Attempts)
	require.Equal(t, "dingtalk: errcode 40035: invalid", d.Error)

	_, err = e.svc.TestChannel(ctx, admin, "nch_missing")
	require.True(t, apperr.IsNotFound(err))

	page, err := e.svc.ListAlertEvents(ctx, AlertQuery{TenantID: e.tenantID, IncludeTenant: true, NamespaceIDs: []string{e.ns.ID}, Kind: KindTest})
	require.NoError(t, err)
	require.Len(t, page.Events, 2)
	require.Equal(t, SeverityInfo, page.Events[0].Severity)
	require.Len(t, page.Events[0].Deliveries, 1)
	require.Contains(t, e.audit.actions(), AuditChannelTest)
	ch, err := e.svc.GetChannel(ctx, failing.ID)
	require.NoError(t, err)
	require.Equal(t, "dingtalk: errcode 40035: invalid", ch.LastDeliveryStatus)
}

func TestListAlertEvents(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{})
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	step := 0
	clock := base
	e.svc.now = func() time.Time { return clock }
	emit := func(a Alert) string {
		step++
		clock = base.Add(time.Duration(step) * time.Minute)
		id, err := e.svc.emit(ctx, a)
		require.NoError(t, err)
		return id
	}
	tenantAlert := emit(Alert{Kind: KindReportBacklog, Severity: SeverityCritical, TenantID: e.tenantID, Title: "backlog"})
	site1 := emit(siteAlert(e, KindBanSpike, SeverityWarning, ""))
	site2 := emit(Alert{Kind: KindBreakerOpened, Severity: SeverityCritical, TenantID: e.tenantID, NamespaceID: e.ns.ID, SiteID: e.site2.ID, Title: "open"})
	nsAlert := emit(Alert{Kind: KindProxyLowWatermark, Severity: SeverityWarning, TenantID: e.tenantID, NamespaceID: e.ns.ID, Title: "proxies"})
	emit(Alert{Kind: KindReportBacklog, Severity: SeverityCritical, TenantID: "ten_other", Title: "other tenant"})

	ids := func(page AlertPage) []string {
		out := []string{}
		for _, ev := range page.Events {
			out = append(out, ev.ID)
		}
		return out
	}
	list := func(q AlertQuery) AlertPage {
		q.TenantID = e.tenantID
		page, err := e.svc.ListAlertEvents(ctx, q)
		require.NoError(t, err)
		return page
	}

	require.Equal(t, []string{nsAlert, site2, site1, tenantAlert}, ids(list(AlertQuery{IncludeTenant: true, NamespaceIDs: []string{e.ns.ID}})))
	require.Equal(t, []string{nsAlert, site2, site1}, ids(list(AlertQuery{NamespaceIDs: []string{e.ns.ID}})))
	require.Equal(t, []string{site1}, ids(list(AlertQuery{SiteIDs: []string{e.site.ID}})))
	require.Equal(t, []string{tenantAlert}, ids(list(AlertQuery{IncludeTenant: true})))
	require.Empty(t, ids(list(AlertQuery{})))
	require.Equal(t, []string{site2}, ids(list(AlertQuery{NamespaceIDs: []string{e.ns.ID}, SiteID: e.site2.ID})))
	require.Equal(t, []string{site2}, ids(list(AlertQuery{IncludeTenant: true, NamespaceIDs: []string{e.ns.ID}, NamespaceID: e.ns.ID, Kind: KindBreakerOpened})))
	require.Equal(t, []string{site2, tenantAlert}, ids(list(AlertQuery{IncludeTenant: true, NamespaceIDs: []string{e.ns.ID}, Severity: SeverityCritical})))
	from, to := base.Add(2*time.Minute), base.Add(4*time.Minute)
	require.Equal(t, []string{site2, site1}, ids(list(AlertQuery{IncludeTenant: true, NamespaceIDs: []string{e.ns.ID}, From: &from, To: &to})))

	// Keyset pagination.
	var all []string
	q := AlertQuery{IncludeTenant: true, NamespaceIDs: []string{e.ns.ID}, Limit: 1}
	for {
		page := list(q)
		all = append(all, ids(page)...)
		if !page.More {
			break
		}
		last := page.Events[len(page.Events)-1]
		q.AfterAt, q.AfterID = &last.CreatedAt, last.ID
	}
	require.Equal(t, []string{nsAlert, site2, site1, tenantAlert}, all)

	_, err := e.svc.ListAlertEvents(ctx, AlertQuery{})
	require.Equal(t, apperr.ReasonInvalidArgument, apperr.ReasonOf(err))
	_, err = e.svc.ListAlertEvents(ctx, AlertQuery{TenantID: e.tenantID, IncludeTenant: true, Kind: "nope"})
	require.Equal(t, apperr.ReasonInvalidArgument, apperr.ReasonOf(err))
}

// counterValue reads a Prometheus counter.
func counterValue(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	var m dto.Metric
	require.NoError(t, c.Write(&m))
	return m.GetCounter().GetValue()
}
