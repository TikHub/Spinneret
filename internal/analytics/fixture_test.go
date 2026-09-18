package analytics

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/rueidis"
	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/catalog/catalogtest"
	"github.com/TikHub/Spinneret/internal/store/redis"
	"github.com/TikHub/Spinneret/internal/testutil"
)

// Fixture identifiers.
const (
	tenantID      = "ten_analytics"
	namespaceID   = "ns_analytics"
	siteA         = "sit_shop"
	siteB         = "sit_forum"
	typeA         = "ity_shop_web"
	typeB         = "ity_forum_web"
	egSearch      = "eg_shop_search"
	egFeed        = "eg_shop_feed"
	egAppFeed     = "eg_shop_app_feed"
	egForumSearch = "eg_forum_search"
)

// Hot-state keys of the fixture catalog.
const (
	siteAKey     = int64(11)
	siteBKey     = int64(12)
	egSearchKey  = int64(1101)
	egFeedKey    = int64(1102)
	egAppFeedKey = int64(1103)
	egForumKey   = int64(1201)
)

// env is an integration test environment with a seeded catalog.
type env struct {
	t     *testing.T
	ctx   context.Context
	pool  *pgxpool.Pool
	rdb   rueidis.Client
	keys  redis.Keys
	ch    chdriver.Conn
	cat   *catalogtest.Catalog
	ns    *catalog.Namespace
	now   time.Time
	svc   *Service
	siteA *catalog.Site
	siteB *catalog.Site
}

// newEnv creates PostgreSQL rows (tenant, namespace, sites, identity types),
// the matching catalog snapshot and a service with a fixed clock. withCH
// attaches a ClickHouse database.
func newEnv(t *testing.T, withCH bool) *env {
	t.Helper()
	e := &env{t: t, ctx: context.Background()}
	e.pool = testutil.Postgres(t)
	e.rdb, e.keys = testutil.Redis(t)
	if withCH {
		e.ch = testutil.ClickHouse(t)
	}
	// 30 seconds into the current minute keeps every fixture row inside the
	// partitions created by the migration template.
	e.now = time.Now().UTC().Truncate(time.Minute).Add(30 * time.Second)

	e.exec(`INSERT INTO tenants (id, name) VALUES ($1, 'acme')`, tenantID)
	e.exec(`INSERT INTO namespaces (id, tenant_id, name) VALUES ($1, $2, 'prod')`, namespaceID, tenantID)
	e.exec(`INSERT INTO sites (id, namespace_id, name, display_name, clients) VALUES
		($1, $3, 'shop', 'Shop (example site)', '{web,app}'), ($2, $3, 'forum', 'Forum (example site)', '{web}')`, siteA, siteB, namespaceID)
	e.exec(`INSERT INTO identity_types (id, site_id, client, name) VALUES ($1, $2, 'web', 'cookie'), ($3, $4, 'web', 'cookie')`,
		typeA, siteA, typeB, siteB)

	e.ns = catalogtest.NewNamespace(tenantID, namespaceID, "prod")
	e.siteA = catalogtest.AddSite(e.ns, siteA, "shop", siteAKey, "web", "app")
	e.siteA.DisplayName = "Shop (example site)"
	catalogtest.AddGroup(e.siteA, egSearch, "web", "search", egSearchKey).LowWatermark = 3
	catalogtest.AddGroup(e.siteA, egFeed, "web", "feed", egFeedKey)
	catalogtest.AddGroup(e.siteA, egAppFeed, "app", "feed", egAppFeedKey)
	e.siteB = catalogtest.AddSite(e.ns, siteB, "forum", siteBKey, "web")
	e.siteB.Paused = true
	catalogtest.AddGroup(e.siteB, egForumSearch, "web", "search", egForumKey)
	e.cat = catalogtest.New(e.ns)

	e.svc = New(e.pool, e.ch, e.rdb, e.keys, e.cat, nil,
		WithClock(func() time.Time { return e.now }), WithReportShards(3))
	return e
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	_, err := e.pool.Exec(e.ctx, sql, args...)
	require.NoError(e.t, err)
}

// identity inserts an identity and returns its hot-state key.
func (e *env) identity(id, siteID, typeID, client, state, region, accountID string, banUntil *time.Time) int64 {
	e.t.Helper()
	sum := sha256.Sum256([]byte(id))
	var account any
	if accountID != "" {
		account = accountID
	}
	var hkey int64
	err := e.pool.QueryRow(e.ctx, `INSERT INTO identities (id, site_id, client, type_id, state, region, account_id, ban_until, unique_hash, payload_hash)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9) RETURNING hkey`,
		id, siteID, client, typeID, state, region, account, banUntil, sum[:]).Scan(&hkey)
	require.NoError(e.t, err)
	return hkey
}

// account inserts an account and returns its hot-state key.
func (e *env) account(id, siteID, ref string) int64 {
	e.t.Helper()
	var hkey int64
	require.NoError(e.t, e.pool.QueryRow(e.ctx,
		`INSERT INTO accounts (id, site_id, external_ref) VALUES ($1, $2, $3) RETURNING hkey`, id, siteID, ref).Scan(&hkey))
	return hkey
}

// proxy inserts a proxy of the fixture namespace.
func (e *env) proxy(id, state string) {
	e.t.Helper()
	sum := sha256.Sum256([]byte(id))
	e.exec(`INSERT INTO proxies (id, namespace_id, scheme, host, port, display_url, url_hash, url_ciphertext, url_wrapped_dek, url_kek_id, state)
		VALUES ($1, $2, 'http', 'proxy.local', 8080, 'http://proxy.local:8080', $3, '\x00', '\x00', 'k1', $4)`,
		id, namespaceID, sum[:], state)
}

// acquireStat adds an acquire_stats_minutely row.
func (e *env) acquireStat(bucket time.Time, siteID, egID, result string, count int64) {
	e.t.Helper()
	e.exec(`INSERT INTO acquire_stats_minutely (bucket, namespace_id, site_id, endpoint_group_id, result, count, duration_us_sum)
		VALUES ($1, $2, $3, $4, $5, $6, 0)`, bucket, namespaceID, siteID, egID, result, count)
}

// outcomeStat adds an outcome_stats_minutely row.
func (e *env) outcomeStat(bucket time.Time, siteID, egID, proxyID, outcome string, count, latencySum int64) {
	e.t.Helper()
	e.exec(`INSERT INTO outcome_stats_minutely (bucket, namespace_id, site_id, endpoint_group_id, proxy_id, outcome, count, latency_ms_sum, response_bytes_sum)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 0)`, bucket, namespaceID, siteID, egID, proxyID, outcome, count, latencySum)
}

// redisDo runs commands and fails the test on error.
func (e *env) redisDo(cmds ...rueidis.Completed) {
	e.t.Helper()
	for _, res := range e.rdb.DoMulti(e.ctx, cmds...) {
		require.NoError(e.t, res.Error())
	}
}

// allScope reads every site of the fixture namespace.
func allScope() Scope {
	return Scope{NamespaceID: namespaceID, AllSites: true}
}

func ptr[T any](v T) *T {
	return &v
}

func groupKey(client, name string) catalog.GroupKey {
	return catalog.GroupKey{Client: client, Name: name}
}

func testKeys() redis.Keys {
	return redis.NewKeys("sp_test")
}
