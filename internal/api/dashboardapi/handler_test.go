package dashboardapi

import (
	"context"
	"crypto/sha256"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/analytics"
	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/catalog/catalogtest"
	chstore "github.com/Evil0ctal/Spinneret/internal/store/clickhouse"
	"github.com/Evil0ctal/Spinneret/internal/testutil"
)

const (
	tenantID    = "ten_dash"
	otherTenant = "ten_dash_other"
	namespaceID = "ns_dash"
	siteA       = "sit_dash_a"
	siteB       = "sit_dash_b"
	egA         = "eg_dash_a_search"
	egB         = "eg_dash_b_search"
	siteAKey    = int64(21)
	siteBKey    = int64(22)
	egAKey      = int64(2101)
	egBKey      = int64(2201)
)

type env struct {
	t      *testing.T
	ctx    context.Context
	pool   *pgxpool.Pool
	now    time.Time
	h      *Handler
	noCH   *Handler
	ns     *catalog.Namespace
	idtA   string
	idtB   string
	bucket time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, ctx: context.Background()}
	pool := testutil.Postgres(t)
	e.pool = pool
	rdb, keys := testutil.Redis(t)
	ch := testutil.ClickHouse(t)
	e.now = time.Now().UTC().Truncate(time.Minute).Add(20 * time.Second)
	e.bucket = e.now.Truncate(time.Minute).Add(-2 * time.Minute)

	exec := func(sql string, args ...any) {
		t.Helper()
		_, err := pool.Exec(e.ctx, sql, args...)
		require.NoError(t, err)
	}
	exec(`INSERT INTO tenants (id, name) VALUES ($1, 'dash')`, tenantID)
	exec(`INSERT INTO namespaces (id, tenant_id, name) VALUES ($1, $2, 'prod')`, namespaceID, tenantID)
	exec(`INSERT INTO sites (id, namespace_id, name) VALUES ($1, $3, 'alpha'), ($2, $3, 'beta')`, siteA, siteB, namespaceID)
	exec(`INSERT INTO identity_types (id, site_id, client, name) VALUES ('ity_a', $1, 'web', 't'), ('ity_b', $2, 'web', 't')`, siteA, siteB)
	identity := func(id, siteID, typeID string) int64 {
		sum := sha256.Sum256([]byte(id))
		var hkey int64
		require.NoError(t, pool.QueryRow(e.ctx, `INSERT INTO identities (id, site_id, client, type_id, state, unique_hash, payload_hash)
			VALUES ($1, $2, 'web', $3, 'active', $4, $4) RETURNING hkey`, id, siteID, typeID, sum[:]).Scan(&hkey))
		return hkey
	}
	e.idtA, e.idtB = "idt_dash_a", "idt_dash_b"
	hkeyA := identity(e.idtA, siteA, "ity_a")
	identity(e.idtB, siteB, "ity_b")

	for _, site := range []struct{ id, eg string }{{siteA, egA}, {siteB, egB}} {
		exec(`INSERT INTO acquire_stats_minutely (bucket, namespace_id, site_id, endpoint_group_id, result, count) VALUES ($1, $2, $3, $4, 'ok', 60)`,
			e.bucket, namespaceID, site.id, site.eg)
		exec(`INSERT INTO outcome_stats_minutely (bucket, namespace_id, site_id, endpoint_group_id, outcome, count, latency_ms_sum) VALUES ($1, $2, $3, $4, 'success', 30, 300)`,
			e.bucket, namespaceID, site.id, site.eg)
		exec(`INSERT INTO risk_events (id, created_at, tenant_id, namespace_id, site_id, endpoint_group_id, outcome, markers, started_at)
			VALUES ($1, $2, $3, $4, $5, $6, 'captcha', '{m}', $2)`, "rsk_"+site.id, e.bucket, tenantID, namespaceID, site.id, site.eg)
	}
	exec(`INSERT INTO risk_events (id, created_at, tenant_id, namespace_id, site_id, endpoint_group_id, outcome)
		VALUES ('rsk_second', $1, $2, $3, $4, $5, 'banned')`, e.bucket.Add(-time.Minute), tenantID, namespaceID, siteA, egA)
	exec(`INSERT INTO node_stats_minutely (bucket, namespace_id, node, acquires, reports, abandoned, rejected) VALUES ($1, $2, 'n1', 10, 8, 2, 1)`,
		e.bucket, namespaceID)

	nowMs := e.now.UnixMilli()
	for _, res := range rdb.DoMulti(e.ctx,
		rdb.B().Zadd().Key(keys.Ready(siteAKey, egAKey)).ScoreMember().ScoreMember(float64(nowMs-1), itoa(hkeyA)).Build(),
		rdb.B().Hset().Key(keys.Health(siteAKey, egAKey)).FieldValue().FieldValue(itoa(hkeyA), "40.00|"+itoa(nowMs)+"|5|0|0|0|0|0").Build(),
	) {
		require.NoError(t, res.Error())
	}

	batch, err := ch.PrepareBatch(e.ctx, "INSERT INTO "+chstore.ReportEventsTable+
		" (event_time, received_at, namespace_id, site_id, site, client, endpoint_group, report_id, lease_id, outcome, latency_ms, http_status, markers)")
	require.NoError(t, err)
	for i, site := range []struct{ id, name string }{{siteA, "alpha"}, {siteA, "alpha"}, {siteB, "beta"}} {
		at := e.bucket.Add(time.Duration(i) * time.Second)
		require.NoError(t, batch.Append(at, at, namespaceID, site.id, site.name, "web", "search",
			"rep_"+itoa(int64(i)), "lse_1", "success", uint32(100*(i+1)), uint16(200), []string{}))
	}
	require.NoError(t, batch.Send())

	e.ns = catalogtest.NewNamespace(tenantID, namespaceID, "prod")
	a := catalogtest.AddSite(e.ns, siteA, "alpha", siteAKey, "web")
	catalogtest.AddGroup(a, egA, "web", "search", egAKey)
	b := catalogtest.AddSite(e.ns, siteB, "beta", siteBKey, "web")
	catalogtest.AddGroup(b, egB, "web", "search", egBKey)
	cat := catalogtest.New(e.ns)

	clock := analytics.WithClock(func() time.Time { return e.now })
	e.h = New(analytics.New(pool, ch, rdb, keys, cat, nil, clock, analytics.WithReportShards(1)), cat)
	e.noCH = New(analytics.New(pool, nil, rdb, keys, cat, nil, clock, analytics.WithReportShards(1)), cat)
	return e
}

func itoa(v int64) string {
	return strconv.FormatInt(v, 10)
}

func user(role authz.Role, siteIDs ...string) *authz.Principal {
	return &authz.Principal{
		Kind: authz.KindUser, ID: "usr_" + string(role), Name: string(role), TenantID: tenantID,
		Bindings: []authz.Binding{{ID: "rb_1", TenantID: tenantID, Role: role, NamespaceID: namespaceID, SiteIDs: siteIDs}},
	}
}

func token(t *testing.T, scopes ...string) *authz.Principal {
	t.Helper()
	parsed, err := authz.ParseScopes(scopes)
	require.NoError(t, err)
	return &authz.Principal{Kind: authz.KindToken, ID: "tok_1", Name: "node", TenantID: tenantID,
		NamespaceID: namespaceID, NamespaceName: "prod", Scopes: parsed}
}

func as(p *authz.Principal) context.Context {
	return authz.WithPrincipal(context.Background(), p)
}

func requireReason(t *testing.T, err error, reason apperr.Reason) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, reason, apperr.ReasonOf(err), "error: %v", err)
}
