package notify

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/redis/rueidis"
	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/catalog/catalogtest"
	"github.com/TikHub/Spinneret/internal/jobs"
	"github.com/TikHub/Spinneret/internal/pkg/idgen"
	"github.com/TikHub/Spinneret/internal/store/redis"
)

func alertsByKind(t *testing.T, e *env, tenantID string) map[string][]AlertEvent {
	t.Helper()
	page, err := e.svc.ListAlertEvents(context.Background(), AlertQuery{
		TenantID: tenantID, IncludeTenant: true, NamespaceIDs: namespaceIDs(e, tenantID), Limit: 500,
	})
	require.NoError(t, err)
	out := map[string][]AlertEvent{}
	for _, ev := range page.Events {
		out[ev.Kind] = append(out[ev.Kind], ev)
	}
	return out
}

func namespaceIDs(e *env, tenantID string) []string {
	var ids []string
	for _, ns := range e.cat.Namespaces(tenantID) {
		ids = append(ids, ns.ID)
	}
	return ids
}

func TestEvaluateJobDefinition(t *testing.T) {
	t.Parallel()
	svc := New(Config{}, nil, nil, nil, redis.NewKeys("t"), catalogtest.New(), nil, nil, nil, nil)
	job := svc.EvaluateJob()
	require.NoError(t, job.Validate())
	require.Equal(t, EvaluateJobName, job.Name)
	require.Equal(t, 30*time.Second, job.Interval)
	require.Equal(t, jobs.Leader, job.Mode)
}

func TestEvaluateIdentityLowWatermark(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{})
	ctx := context.Background()
	low := catalogtest.AddGroup(e.site, "eg_low", "web", "search", 1001)
	low.LowWatermark = 5
	ok := catalogtest.AddGroup(e.site, "eg_ok", "web", "detail", 1002)
	ok.LowWatermark = 2
	catalogtest.AddGroup(e.site, "eg_none", "web", "feed", 1003)

	now := time.Now().UnixMilli()
	cmds := rueidis.Commands{}
	for i := range 3 {
		cmds = append(cmds, e.rdb.B().Zadd().Key(e.keys.Ready(e.site.Key, low.Key)).ScoreMember().
			ScoreMember(float64(now-1000), strconv.Itoa(i)).Build())
	}
	for i := range 4 {
		cmds = append(cmds, e.rdb.B().Zadd().Key(e.keys.Ready(e.site.Key, low.Key)).ScoreMember().
			ScoreMember(float64(now+600_000), strconv.Itoa(100+i)).Build())
	}
	for i := range 2 {
		cmds = append(cmds, e.rdb.B().Zadd().Key(e.keys.Ready(e.site.Key, ok.Key)).ScoreMember().
			ScoreMember(float64(now-1000), strconv.Itoa(i)).Build())
	}
	for _, res := range e.rdb.DoMulti(ctx, cmds...) {
		require.NoError(t, res.Error())
	}

	alerts, err := e.svc.ruleIdentityLowWatermark(ctx)
	require.NoError(t, err)
	require.Len(t, alerts, 1)
	a := alerts[0]
	require.Equal(t, KindIdentityLowWatermark, a.Kind)
	require.Equal(t, SeverityWarning, a.Severity)
	require.Equal(t, e.site.ID, a.SiteID)
	require.EqualValues(t, 3, a.Details["available"])
	require.EqualValues(t, 5, a.Details["low_watermark"])
	require.Equal(t, "group:eg_low", a.DedupKey)

	require.NoError(t, e.svc.evaluate(ctx))
	require.NoError(t, e.svc.evaluate(ctx))
	require.Len(t, alertsByKind(t, e, e.tenantID)[KindIdentityLowWatermark], 1, "de-duplicated per group")
}

func TestEvaluateProxyLowWatermark(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{})
	ctx := context.Background()
	staging := e.seedNamespace(e.tenantID, "staging")
	small := e.seedNamespace(e.tenantID, "small")
	seedProxies := func(nsID string, states ...string) {
		for i, state := range states {
			hash := sha256.Sum256([]byte(fmt.Sprintf("%s-%d", nsID, i)))
			e.exec(`INSERT INTO proxies (id, namespace_id, scheme, host, port, display_url, url_hash, url_ciphertext,
				url_wrapped_dek, url_kek_id, state) VALUES ($1, $2, 'http', 'p', 8080, 'http://p:8080', $3, '\x00', '\x00', 'k', $4)`,
				idgen.New(idgen.Proxy), nsID, hash[:], state)
		}
	}
	seedProxies(e.ns.ID, "active", "dead", "dead", "dead", "dead", "dead", "retired")
	seedProxies(staging.ID, "active", "active", "dead", "dead", "dead", "disabled")
	seedProxies(small.ID, "dead", "dead", "dead", "dead")

	alerts, err := e.svc.ruleProxyLowWatermark(ctx)
	require.NoError(t, err)
	require.Len(t, alerts, 1)
	a := alerts[0]
	require.Equal(t, e.ns.ID, a.NamespaceID)
	require.Equal(t, e.tenantID, a.TenantID)
	require.Empty(t, a.SiteID)
	require.EqualValues(t, 1, a.Details["active"])
	require.EqualValues(t, 5, a.Details["dead"])
	require.EqualValues(t, 6, a.Details["total"])
	require.Equal(t, 0.17, a.Details["active_ratio"])
}

func TestEvaluateBanSpike(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{})
	ctx := context.Background()
	now := time.Now().UTC()
	seedBans := func(siteID string, n int, at time.Time, action string, shadow bool) {
		for range n {
			e.exec(`INSERT INTO state_events (id, created_at, tenant_id, namespace_id, site_id, subject_kind, subject_id, action, shadow)
				VALUES ($1, $2, $3, $4, $5, 'identity', 'idt_x', $6, $7)`,
				idgen.New(idgen.StateEvent), at, e.tenantID, e.ns.ID, siteID, action, shadow)
		}
	}
	// site: 12 recent bans, empty baseline → threshold 10 → spike.
	seedBans(e.site.ID, 12, now.Add(-time.Minute), "ban", false)
	seedBans(e.site.ID, 30, now.Add(-time.Minute), "cooldown", false)
	seedBans(e.site.ID, 30, now.Add(-time.Minute), "ban", true)
	// site2: 11 recent bans but 60 in the previous hour (avg 5 → threshold 15).
	seedBans(e.site2.ID, 11, now.Add(-2*time.Minute), "ban", false)
	seedBans(e.site2.ID, 60, now.Add(-30*time.Minute), "ban", false)
	// Bans older than 65 minutes do not count.
	seedBans(e.site.ID, 100, now.Add(-2*time.Hour), "ban", false)

	alerts, err := e.svc.ruleBanSpike(ctx)
	require.NoError(t, err)
	require.Len(t, alerts, 1)
	a := alerts[0]
	require.Equal(t, e.site.ID, a.SiteID)
	require.EqualValues(t, 12, a.Details["bans_5m"])
	require.Equal(t, 10.0, a.Details["threshold"])
	require.Equal(t, "Ban spike on shop", a.Title)

	// The baseline is cached: new baseline rows are ignored until it refreshes.
	seedBans(e.site.ID, 1000, now.Add(-20*time.Minute), "ban", false)
	alerts, err = e.svc.ruleBanSpike(ctx)
	require.NoError(t, err)
	require.Len(t, alerts, 1)
	e.svc.banMu.Lock()
	e.svc.banBaseline.computedAt = now.Add(-banBaselineRefresh - time.Second)
	e.svc.banMu.Unlock()
	alerts, err = e.svc.ruleBanSpike(ctx)
	require.NoError(t, err)
	require.Empty(t, alerts)

	// No namespaces → nothing to do.
	empty := New(Config{}, e.pool, nil, e.rdb, e.keys, catalogtest.New(), nil, nil, nil, nil)
	alerts, err = empty.ruleBanSpike(ctx)
	require.NoError(t, err)
	require.Empty(t, alerts)
}

func TestEvaluateReportBacklog(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{ReportShards: 3})
	ctx := context.Background()
	other := e.seedTenant("globex")
	unsubscribed := e.seedTenant("initech")
	e.webhookChannel("backlog", "", "https://hooks.example.com/a", []string{KindReportBacklog}, nil, SeverityCritical)
	_, err := e.svc.CreateChannel(ctx, admin, ChannelInput{
		TenantID: other, Name: "all", Kind: ChannelWeCom, Config: map[string]any{"webhook_url": "https://x"},
		EventTypes: []string{KindReportBacklog, KindBanSpike}, Enabled: true,
	})
	require.NoError(t, err)
	_, err = e.svc.CreateChannel(ctx, admin, ChannelInput{
		TenantID: unsubscribed, Name: "bans", Kind: ChannelWeCom, Config: map[string]any{"webhook_url": "https://x"},
		EventTypes: []string{KindBanSpike}, Enabled: true,
	})
	require.NoError(t, err)

	fill := func(shard, n int) {
		key := e.keys.Stream(shard)
		for start := 0; start < n; start += 5000 {
			cmds := make(rueidis.Commands, 0, 5000)
			for i := start; i < min(start+5000, n); i++ {
				cmds = append(cmds, e.rdb.B().Xadd().Key(key).Id("*").FieldValue().FieldValue("d", "x").Build())
			}
			for _, res := range e.rdb.DoMulti(ctx, cmds...) {
				require.NoError(t, res.Error())
			}
		}
	}
	// Shard 0: no consumer group yet → the whole stream is backlog.
	fill(0, 30_000)
	// Shard 1: group read 5000 entries without acknowledging them → pending 5000, lag 15000.
	fill(1, 20_000)
	require.NoError(t, e.rdb.Do(ctx, e.rdb.B().XgroupCreate().Key(e.keys.Stream(1)).Group(ReportConsumerGroup).Id("0").Build()).Error())
	require.NoError(t, e.rdb.Do(ctx, e.rdb.B().Xreadgroup().Group(ReportConsumerGroup, "c1").Count(5000).
		Streams().Key(e.keys.Stream(1)).Id(">").Build()).Error())
	// Shard 2 does not exist.

	b, err := e.svc.reportBacklog(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 5000, b.pending)
	require.EqualValues(t, 45_000, b.lag)
	require.Equal(t, map[string]int64{"0": 30_000, "1": 20_000}, b.perShard)
	alerts, err := e.svc.ruleReportBacklog(ctx)
	require.NoError(t, err)
	require.Empty(t, alerts, "exactly 50000 is not above the threshold")

	fill(0, 1)
	alerts, err = e.svc.ruleReportBacklog(ctx)
	require.NoError(t, err)
	tenants := map[string]bool{}
	for _, a := range alerts {
		require.Equal(t, KindReportBacklog, a.Kind)
		require.Equal(t, SeverityCritical, a.Severity)
		require.Empty(t, a.NamespaceID)
		require.EqualValues(t, 50_001, a.Details["backlog"])
		tenants[a.TenantID] = true
	}
	require.Equal(t, map[string]bool{e.tenantID: true, other: true}, tenants)

	require.NoError(t, e.svc.evaluate(ctx))
	require.Len(t, alertsByKind(t, e, e.tenantID)[KindReportBacklog], 1)
	require.Len(t, alertsByKind(t, e, other)[KindReportBacklog], 1)
	require.Empty(t, alertsByKind(t, e, unsubscribed)[KindReportBacklog])
}

func TestEvaluateOutcomeRatios(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{})
	ctx := context.Background()
	third := e.seedSite(e.ns, "market")
	bucket := time.Now().UTC().Truncate(time.Minute).Add(-2 * time.Minute)
	seed := func(siteID string, at time.Time, counts map[string]int) {
		for outcome, n := range counts {
			e.exec(`INSERT INTO outcome_stats_minutely (bucket, namespace_id, site_id, endpoint_group_id, proxy_id, outcome, count)
				VALUES ($1, $2, $3, 'eg', '', $4, $5)`, at, e.ns.ID, siteID, outcome, n)
		}
	}
	seed(e.site.ID, bucket, map[string]int{"success": 140, "unknown": 50, "client_error": 10})
	seed(e.site2.ID, bucket, map[string]int{"success": 130, "client_error": 20})
	seed(third.ID, bucket, map[string]int{"unknown": 40, "success": 10})
	seed(third.ID, bucket.Add(-20*time.Minute), map[string]int{"unknown": 400})
	// Rows without a site are skipped.
	seed("", bucket, map[string]int{"unknown": 500})
	// Rows of a namespace missing from the catalog are skipped.
	e.exec(`INSERT INTO outcome_stats_minutely (bucket, namespace_id, site_id, outcome, count) VALUES ($1, 'ns_gone', 'sit_gone', 'unknown', 500)`, bucket)

	alerts, err := e.svc.ruleOutcomeRatios(ctx)
	require.NoError(t, err)
	require.Len(t, alerts, 2)
	bySite := map[string]Alert{}
	for _, a := range alerts {
		bySite[a.SiteID] = a
	}
	unknown := bySite[e.site.ID]
	require.Equal(t, KindUnknownRatioHigh, unknown.Kind)
	require.Equal(t, 0.25, unknown.Details["ratio"])
	require.EqualValues(t, 200, unknown.Details["total"])
	clientErr := bySite[e.site2.ID]
	require.Equal(t, KindClientErrorSpike, clientErr.Kind)
	require.Equal(t, 0.13, clientErr.Details["ratio"])
	require.Equal(t, "Client error spike on forum", clientErr.Title)
}

func TestEvaluateSecretExpiring(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{})
	ctx := context.Background()
	now := time.Now().UTC()
	seed := func(path string, expires *time.Time) string {
		id := idgen.New(idgen.Secret)
		e.exec(`INSERT INTO secrets (id, namespace_id, path, expires_at) VALUES ($1, $2, $3, $4)`, id, e.ns.ID, path, expires)
		return id
	}
	at := func(d time.Duration) *time.Time { v := now.Add(d); return &v }
	soon := seed("api/soon", at(3*24*time.Hour))
	expired := seed("api/expired", at(-2*24*time.Hour))
	seed("api/later", at(30*24*time.Hour))
	seed("api/long-expired", at(-10*24*time.Hour))
	seed("api/never", nil)

	alerts, err := e.svc.ruleSecretExpiring(ctx)
	require.NoError(t, err)
	require.Len(t, alerts, 2)
	byID := map[string]Alert{}
	for _, a := range alerts {
		byID[a.Details["secret_id"].(string)] = a
		require.Equal(t, secretExpiringDedupTTL, a.DedupTTL)
		require.Equal(t, e.ns.ID, a.NamespaceID)
	}
	require.Equal(t, "Secret expiring: api/soon", byID[soon].Title)
	require.Equal(t, false, byID[soon].Details["expired"])
	require.Equal(t, "Secret expired: api/expired", byID[expired].Title)
	require.Equal(t, true, byID[expired].Details["expired"])

	require.NoError(t, e.svc.evaluate(ctx))
	require.NoError(t, e.svc.evaluate(ctx))
	require.Len(t, alertsByKind(t, e, e.tenantID)[KindSecretExpiring], 2)
	pttl, err := e.rdb.Do(ctx, e.rdb.B().Pttl().Key(e.keys.AlertDedup(e.tenantID+":"+KindSecretExpiring+":secret:"+soon)).Build()).AsInt64()
	require.NoError(t, err)
	require.Greater(t, pttl, int64(23*time.Hour/time.Millisecond))
}

func TestEvaluateReportsRuleErrors(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{})
	g := catalogtest.AddGroup(e.site, "eg_low", "web", "search", 1001)
	g.LowWatermark = 1
	e.pool.Close()
	err := e.svc.evaluate(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "rule proxy_low_watermark")
	require.Contains(t, err.Error(), "rule secret_expiring")
	require.Contains(t, err.Error(), "emit identity_low_watermark")
	require.Contains(t, err.Error(), "recover identity_expired alerts")

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, e.svc.evaluate(canceled))
}

func TestGroupBacklogParsing(t *testing.T) {
	t.Parallel()
	require.True(t, isNoSuchKey(fmt.Errorf("ERR no such key")))
	require.False(t, isNoSuchKey(nil))
	require.Equal(t, 0.0, ratio(1, 0))
	require.Equal(t, 0.33, round2(1.0/3))
	require.Len(t, sortedGroups(catalogtest.AddSite(catalogtest.NewNamespace("t", "n", "n"), "s", "s", 1, "web", "app")), 2)
}

// Alerts suppressed by de-duplication do not count towards the per-rule cap,
// so candidates beyond the cap are not starved while earlier ones stay active.
func TestEmitAllCapCountsStoredAlerts(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{})
	ctx := context.Background()
	alerts := make([]Alert, 0, maxAlertsPerRule+2)
	cmds := make(rueidis.Commands, 0, maxAlertsPerRule)
	for i := range maxAlertsPerRule + 2 {
		a := siteAlert(e, KindIdentityLowWatermark, SeverityWarning, "group:eg_"+strconv.Itoa(i))
		alerts = append(alerts, a)
		if i < maxAlertsPerRule {
			key := e.keys.AlertDedup(e.tenantID + ":" + a.Kind + ":" + a.DedupKey)
			cmds = append(cmds, e.rdb.B().Set().Key(key).Value("1").PxMilliseconds(time.Hour.Milliseconds()).Build())
		}
	}
	for _, res := range e.rdb.DoMulti(ctx, cmds...) {
		require.NoError(t, res.Error())
	}
	require.NoError(t, e.svc.emitAll(ctx, KindIdentityLowWatermark, alerts))
	stored := alertsByKind(t, e, e.tenantID)[KindIdentityLowWatermark]
	require.Len(t, stored, 2, "the candidates after the de-duplicated ones are emitted")

	// The cap still bounds stored alerts per run.
	many := make([]Alert, 0, maxAlertsPerRule+1)
	for i := range maxAlertsPerRule + 1 {
		many = append(many, siteAlert(e, KindBanSpike, SeverityWarning, "site:"+strconv.Itoa(i)))
	}
	require.NoError(t, e.svc.emitAll(ctx, KindBanSpike, many))
	page, err := e.svc.ListAlertEvents(ctx, AlertQuery{TenantID: e.tenantID, NamespaceIDs: []string{e.ns.ID}, Kind: KindBanSpike, Limit: 500})
	require.NoError(t, err)
	require.Len(t, page.Events, maxAlertsPerRule)
	require.False(t, page.More, "exactly maxAlertsPerRule alerts were stored")
}
