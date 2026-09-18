package proxy

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
)

func TestGetProviderStats(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Minute)
	env.svc.now = fixedClock(now)
	proxies := env.importLines(t, env.ns,
		"http://10.8.0.1:80 provider=acme",
		"http://10.8.0.2:80 provider=acme",
		"http://10.8.0.3:80 provider=beta",
		"http://10.8.0.4:80",
	)
	_, err := env.pool.Exec(ctx, `UPDATE proxies SET state = 'dead' WHERE id = $1`, proxies[1].ID)
	require.NoError(t, err)
	_, err = env.pool.Exec(ctx, `UPDATE proxies SET state = 'retired' WHERE id = $1`, proxies[3].ID)
	require.NoError(t, err)

	insert := func(bucket time.Time, site, proxyID, outcome string, count, latency int64) {
		t.Helper()
		_, err := env.pool.Exec(ctx, `INSERT INTO outcome_stats_minutely (bucket, namespace_id, site_id, endpoint_group_id, proxy_id, outcome, count, latency_ms_sum)
			VALUES ($1, 'ns_main', $2, 'eg_1', $3, $4, $5, $6)`, bucket, site, proxyID, outcome, count, latency)
		require.NoError(t, err)
	}
	recent := now.Add(-time.Hour)
	insert(recent, env.alpha.ID, proxies[0].ID, "success", 60, 6000)
	insert(recent, env.alpha.ID, proxies[0].ID, "captcha", 20, 4000)
	insert(recent, env.beta.ID, proxies[1].ID, "rate_limited", 10, 1000)
	insert(recent, env.beta.ID, proxies[1].ID, "network_error", 10, 9000)
	insert(recent, env.alpha.ID, proxies[2].ID, "success", 5, 500)
	insert(recent, env.alpha.ID, "", "success", 1000, 1000)
	insert(now.Add(-30*time.Hour), env.alpha.ID, proxies[2].ID, "success", 999, 999)

	stats, err := env.svc.GetProviderStats(ctx, viewerUser(), env.ns, StatsRequest{})
	require.NoError(t, err)
	require.Len(t, stats, 3)
	acme := stats[0]
	require.Equal(t, "acme", acme.Provider)
	require.Equal(t, 2, acme.Proxies)
	require.Equal(t, 1, acme.Active)
	require.Equal(t, 1, acme.Dead)
	require.EqualValues(t, 100, acme.Requests)
	require.InDelta(t, 0.6, acme.SuccessRatio, 1e-9)
	require.InDelta(t, 0.3, acme.RiskRatio, 1e-9)
	require.InDelta(t, 200, acme.AvgLatencyMs, 1e-9)
	require.Equal(t, ProviderStats{Provider: "beta", Proxies: 1, Active: 1, Requests: 5, SuccessRatio: 1, AvgLatencyMs: 100}, stats[1])
	require.Equal(t, ProviderStats{Provider: ""}, stats[2], "retired proxies are not counted")

	t.Run("site filter", func(t *testing.T) {
		stats, err := env.svc.GetProviderStats(ctx, adminUser(), env.ns, StatsRequest{Site: "beta"})
		require.NoError(t, err)
		require.EqualValues(t, 20, stats[0].Requests)
		require.Equal(t, "acme", stats[0].Provider)
		require.InDelta(t, 0.5, stats[0].RiskRatio, 1e-9)
	})

	t.Run("explicit range", func(t *testing.T) {
		start, end := now.Add(-48*time.Hour), now.Add(-24*time.Hour)
		stats, err := env.svc.GetProviderStats(ctx, adminUser(), env.ns, StatsRequest{Start: &start, End: &end})
		require.NoError(t, err)
		require.Equal(t, "beta", stats[0].Provider)
		require.EqualValues(t, 999, stats[0].Requests)
		_, err = env.svc.GetProviderStats(ctx, adminUser(), env.ns, StatsRequest{Start: &end, End: &start})
		requireReason(t, err, apperr.ReasonInvalidArgument)
	})

	t.Run("site restricted principal only sees its sites", func(t *testing.T) {
		stats, err := env.svc.GetProviderStats(ctx, siteOperator(env.ns.ID, env.alpha.ID), env.ns, StatsRequest{})
		require.NoError(t, err)
		require.Equal(t, "acme", stats[0].Provider)
		require.EqualValues(t, 80, stats[0].Requests)
		_, err = env.svc.GetProviderStats(ctx, siteOperator(env.ns.ID, env.alpha.ID), env.ns, StatsRequest{Site: "beta"})
		requireReason(t, err, apperr.ReasonPermissionDenied)
	})

	t.Run("errors", func(t *testing.T) {
		_, err := env.svc.GetProviderStats(ctx, adminUser(), env.ns, StatsRequest{Site: "nope"})
		requireReason(t, err, apperr.ReasonSiteUnknown)
		_, err = env.svc.GetProviderStats(ctx, foreignUser(), env.ns, StatsRequest{})
		requireReason(t, err, apperr.ReasonPermissionDenied)
		_, err = env.svc.GetProviderStats(ctx, tokenPrincipal(t, "report:write"), env.ns, StatsRequest{})
		requireReason(t, err, apperr.ReasonScopeMissing)
	})
}
