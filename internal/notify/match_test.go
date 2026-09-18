package notify

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTargetMatches(t *testing.T) {
	t.Parallel()
	siteAlert := Alert{Kind: KindBanSpike, Severity: SeverityWarning, TenantID: "ten_1", NamespaceID: "ns_1", SiteID: "sit_1"}
	nsAlert := Alert{Kind: KindProxyLowWatermark, Severity: SeverityWarning, TenantID: "ten_1", NamespaceID: "ns_1"}
	tenantAlert := Alert{Kind: KindReportBacklog, Severity: SeverityCritical, TenantID: "ten_1"}
	tests := []struct {
		name   string
		target target
		alert  Alert
		want   bool
	}{
		{"tenant channel gets site alerts", target{minSeverity: SeverityInfo}, siteAlert, true},
		{"tenant channel gets tenant alerts", target{minSeverity: SeverityInfo}, tenantAlert, true},
		{"namespace channel gets its alerts", target{namespaceID: "ns_1", minSeverity: SeverityInfo}, siteAlert, true},
		{"namespace channel skips other namespaces", target{namespaceID: "ns_2", minSeverity: SeverityInfo}, siteAlert, false},
		{"namespace channel gets tenant-level alerts", target{namespaceID: "ns_2", eventTypes: []string{KindReportBacklog}, minSeverity: SeverityInfo}, tenantAlert, true},
		{"site channel gets its site", target{namespaceID: "ns_1", siteIDs: []string{"sit_1"}, minSeverity: SeverityInfo}, siteAlert, true},
		{"site channel skips other sites", target{namespaceID: "ns_1", siteIDs: []string{"sit_2"}, minSeverity: SeverityInfo}, siteAlert, false},
		{"site channel gets namespace alerts", target{namespaceID: "ns_1", siteIDs: []string{"sit_2"}, minSeverity: SeverityInfo}, nsAlert, true},
		{"empty event types means all", target{minSeverity: SeverityInfo}, nsAlert, true},
		{"kind not subscribed", target{eventTypes: []string{KindBreakerOpened}, minSeverity: SeverityInfo}, nsAlert, false},
		{"severity below minimum", target{minSeverity: SeverityCritical}, nsAlert, false},
		{"severity at minimum", target{minSeverity: SeverityWarning}, nsAlert, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, tt.target.matches(tt.alert))
		})
	}
}

func TestChannelCache(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	c := newChannelCache(10 * time.Second)
	targets := []target{{id: "nch_1"}}

	_, gen, ok := c.get("ten_1", now)
	require.False(t, ok)
	c.put("ten_1", targets, now, gen)
	got, _, ok := c.get("ten_1", now.Add(time.Second))
	require.True(t, ok)
	require.Equal(t, targets, got)

	// Expired entries are dropped (they hold decrypted settings).
	_, _, ok = c.get("ten_1", now.Add(10*time.Second))
	require.False(t, ok)
	c.mu.Lock()
	require.Empty(t, c.entries)
	c.mu.Unlock()

	// A load that started before an invalidation does not store its stale result.
	_, stale, _ := c.get("ten_1", now)
	c.invalidate("ten_1")
	c.put("ten_1", targets, now, stale)
	_, fresh, ok := c.get("ten_1", now)
	require.False(t, ok)
	c.put("ten_1", targets, now, fresh)
	_, _, ok = c.get("ten_1", now)
	require.True(t, ok)

	// A clock moving backwards invalidates the entry.
	_, _, ok = c.get("ten_1", now.Add(-time.Second))
	require.False(t, ok)

	// The cache is bounded.
	for i := range maxCachedTenants + 1 {
		_, g, _ := c.get("tenant", now)
		c.put(string(rune('a'+i%26))+time.Duration(i).String(), targets, now, g)
	}
	c.mu.Lock()
	require.LessOrEqual(t, len(c.entries), maxCachedTenants)
	c.mu.Unlock()
}

func TestTenantAlertReachesNamespaceChannels(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{})
	ctx := context.Background()
	hook := newProvider(t)
	e.webhookChannel("ns-backlog", e.ns.ID, hook.URL, []string{KindReportBacklog}, nil, SeverityCritical)
	e.startService()

	id, err := e.svc.emit(ctx, Alert{Kind: KindReportBacklog, Severity: SeverityCritical, TenantID: e.tenantID, Title: "backlog"})
	require.NoError(t, err)
	deliveries := e.waitDeliveries(id, 1)
	require.True(t, deliveries[0].OK, deliveries[0].Error)
	require.Equal(t, "ns-backlog", deliveries[0].ChannelName)
}

func TestChannelMutationInvalidatesMatching(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{ChannelCacheTTL: time.Hour})
	ctx := context.Background()
	hook := newProvider(t)
	ch := e.webhookChannel("hook", e.ns.ID, hook.URL, []string{KindBanSpike}, nil, SeverityInfo)

	targets, err := e.svc.matchTargets(ctx, siteAlert(e, KindBanSpike, SeverityWarning, ""))
	require.NoError(t, err)
	require.Len(t, targets, 1)

	_, err = e.svc.UpdateChannel(ctx, admin, ch.ID, ChannelUpdate{Name: "hook", EventTypes: []string{KindBanSpike}, Enabled: false})
	require.NoError(t, err)
	targets, err = e.svc.matchTargets(ctx, siteAlert(e, KindBanSpike, SeverityWarning, ""))
	require.NoError(t, err)
	require.Empty(t, targets, "a disabled channel stops matching immediately on this instance")
}
