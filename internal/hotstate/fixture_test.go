package hotstate

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/rueidis"
	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/catalog/catalogtest"
	"github.com/Evil0ctal/Spinneret/internal/identity"
	"github.com/Evil0ctal/Spinneret/internal/pkg/idgen"
	spsite "github.com/Evil0ctal/Spinneret/internal/site"
	"github.com/Evil0ctal/Spinneret/internal/store/redis"
	"github.com/Evil0ctal/Spinneret/internal/testutil"
)

// fixture wires a Syncer to a fresh PostgreSQL database, a Redis key prefix
// and an in-memory catalog whose snapshots mirror the rows inserted by the
// helpers (with real hot-state keys).
type fixture struct {
	t      *testing.T
	ctx    context.Context
	pool   *pgxpool.Pool
	rdb    rueidis.Client
	keys   redis.Keys
	cat    *catalogtest.Catalog
	ns     *catalog.Namespace
	syncer *Syncer
	now    time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := testutil.Postgres(t)
	rdb, keys := testutil.Redis(t)
	f := &fixture{t: t, ctx: context.Background(), pool: pool, rdb: rdb, keys: keys}
	tenantID, nsID := idgen.New(idgen.Tenant), idgen.New(idgen.Namespace)
	f.exec(`INSERT INTO tenants (id, name) VALUES ($1, $2)`, tenantID, "t-"+tenantID[len(tenantID)-8:])
	f.exec(`INSERT INTO namespaces (id, tenant_id, name) VALUES ($1, $2, 'prod')`, nsID, tenantID)
	f.ns = catalogtest.NewNamespace(tenantID, nsID, "prod")
	f.cat = catalogtest.New(f.ns)
	f.syncer = NewSyncer(pool, rdb, keys, f.cat, slog.New(slog.DiscardHandler))
	f.now = time.Now().Truncate(time.Millisecond)
	f.syncer.now = func() time.Time { return f.now }
	return f
}

func (f *fixture) exec(sql string, args ...any) {
	f.t.Helper()
	_, err := f.pool.Exec(f.ctx, sql, args...)
	require.NoError(f.t, err)
}

// addNamespace inserts another namespace of the same tenant and registers it in the catalog.
func (f *fixture) addNamespace(name string) *catalog.Namespace {
	f.t.Helper()
	id := idgen.New(idgen.Namespace)
	f.exec(`INSERT INTO namespaces (id, tenant_id, name) VALUES ($1, $2, $3)`, id, f.ns.TenantID, name)
	ns := catalogtest.NewNamespace(f.ns.TenantID, id, name)
	f.cat.Put(ns)
	return ns
}

// addSite inserts a site with a "_default" endpoint group per client.
func (f *fixture) addSite(ns *catalog.Namespace, name string, clients ...string) *catalog.Site {
	f.t.Helper()
	if len(clients) == 0 {
		clients = []string{"web"}
	}
	id := idgen.New(idgen.Site)
	var key int64
	require.NoError(f.t, f.pool.QueryRow(f.ctx,
		`INSERT INTO sites (id, namespace_id, name, clients) VALUES ($1, $2, $3, $4) RETURNING hkey`,
		id, ns.ID, name, clients).Scan(&key))
	s := &catalog.Site{
		ID: id, Name: name, DisplayName: name, Key: key, NamespaceID: ns.ID, NamespaceName: ns.Name,
		TenantID: ns.TenantID, Clients: append([]string(nil), clients...),
		Groups:            map[catalog.GroupKey]*catalog.EndpointGroup{},
		GroupsByID:        map[string]*catalog.EndpointGroup{},
		GroupsByKey:       map[int64]*catalog.EndpointGroup{},
		Matchers:          map[string]*spsite.Matcher{},
		IdentityTypes:     map[string]*identity.CompiledType{},
		IdentityTypesByID: map[string]*identity.CompiledType{},
	}
	ns.Sites[name] = s
	ns.SitesByID[id] = s
	for _, c := range clients {
		f.addGroup(s, c, spsite.DefaultGroup)
	}
	return s
}

// addGroup inserts an endpoint group and adds it to the site snapshot.
func (f *fixture) addGroup(s *catalog.Site, client, name string) *catalog.EndpointGroup {
	f.t.Helper()
	id := idgen.New(idgen.EndpointGroup)
	var key int64
	require.NoError(f.t, f.pool.QueryRow(f.ctx,
		`INSERT INTO endpoint_groups (id, site_id, client, name) VALUES ($1, $2, $3, $4) RETURNING hkey`,
		id, s.ID, client, name).Scan(&key))
	g := catalogtest.AddGroup(s, id, client, name, key)
	for _, typ := range s.IdentityTypesByID {
		if typ.Client == client {
			g.IdentityTypeIDs = append(g.IdentityTypeIDs, typ.ID)
		}
	}
	return g
}

// removeGroup deletes an endpoint group from PostgreSQL and the snapshot.
func (f *fixture) removeGroup(s *catalog.Site, g *catalog.EndpointGroup) {
	f.t.Helper()
	f.exec(`DELETE FROM endpoint_groups WHERE id = $1`, g.ID)
	delete(s.Groups, catalog.GroupKey{Client: g.Client, Name: g.Name})
	delete(s.GroupsByID, g.ID)
	delete(s.GroupsByKey, g.Key)
}

const typeYAML = `
name: %s
site: %s
client: %s
fields:
  token: { type: string, required: true, sensitive: true }
unique_by: [token]
deliver:
  headers:
    Authorization: "{{ token }}"
`

// addType inserts an identity type eligible for every group of its client.
func (f *fixture) addType(s *catalog.Site, client, name string) *identity.CompiledType {
	f.t.Helper()
	id := idgen.New(idgen.IdentityType)
	f.exec(`INSERT INTO identity_types (id, site_id, client, name, version) VALUES ($1, $2, $3, $4, 3)`,
		id, s.ID, client, name)
	typ := catalogtest.MustCompileType(id, s.ID, 3, fmt.Sprintf(typeYAML, name, s.Name, client))
	catalogtest.AddIdentityType(s, typ)
	return typ
}

// identitySpec describes an identity row to insert.
type identitySpec struct {
	state           string
	banUntil        *time.Time
	quarantineUntil *time.Time
	activatedAt     *time.Time
	accountID       string
	region          string
	payloadVersion  int
}

// addIdentity inserts an identity and returns its ID and hot-state key.
func (f *fixture) addIdentity(s *catalog.Site, typ *identity.CompiledType, spec identitySpec) (string, int64) {
	f.t.Helper()
	id := idgen.New(idgen.Identity)
	if spec.state == "" {
		spec.state = stateActive
	}
	if spec.payloadVersion == 0 {
		spec.payloadVersion = 1
	}
	var account any
	if spec.accountID != "" {
		account = spec.accountID
	}
	var key int64
	require.NoError(f.t, f.pool.QueryRow(f.ctx, `
		INSERT INTO identities (id, site_id, client, type_id, account_id, state, ban_until, quarantine_until,
		                        region, unique_hash, payload_hash, payload_version, activated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13) RETURNING hkey`,
		id, s.ID, typ.Client, typ.ID, account, spec.state, spec.banUntil, spec.quarantineUntil, spec.region,
		randomBytes(), randomBytes(), spec.payloadVersion, spec.activatedAt).Scan(&key))
	return id, key
}

// addAccount inserts an account and returns its ID and hot-state key.
func (f *fixture) addAccount(s *catalog.Site, ref, state string, cooldownUntil *time.Time) (string, int64) {
	f.t.Helper()
	id := idgen.New(idgen.Account)
	var key int64
	require.NoError(f.t, f.pool.QueryRow(f.ctx, `
		INSERT INTO accounts (id, site_id, external_ref, state, cooldown_until)
		VALUES ($1, $2, $3, $4, $5) RETURNING hkey`, id, s.ID, ref, state, cooldownUntil).Scan(&key))
	return id, key
}

// proxySpec describes a proxy row to insert.
type proxySpec struct {
	state         string
	kind          string
	region        string
	provider      string
	tags          []string
	maxConc       int
	cooldownUntil *time.Time
}

// addProxy inserts a proxy of a namespace and returns its ID and hot-state key.
func (f *fixture) addProxy(ns *catalog.Namespace, spec proxySpec) (string, int64) {
	f.t.Helper()
	id := idgen.New(idgen.Proxy)
	if spec.state == "" {
		spec.state = stateActive
	}
	if spec.kind == "" {
		spec.kind = "datacenter"
	}
	if spec.tags == nil {
		spec.tags = []string{}
	}
	if spec.maxConc == 0 {
		spec.maxConc = 1
	}
	var key int64
	require.NoError(f.t, f.pool.QueryRow(f.ctx, `
		INSERT INTO proxies (id, namespace_id, scheme, host, port, display_url, url_hash, url_ciphertext,
		                     url_wrapped_dek, url_kek_id, kind, region, provider, max_concurrency, tags, state,
		                     cooldown_until)
		VALUES ($1, $2, 'http', 'proxy.local', 8080, 'http://proxy.local:8080', $3, $4, $5, 'k1', $6, $7, $8, $9,
		        $10, $11, $12) RETURNING hkey`,
		id, ns.ID, randomBytes(), randomBytes(), randomBytes(), spec.kind, spec.region, spec.provider,
		spec.maxConc, spec.tags, spec.state, spec.cooldownUntil).Scan(&key))
	return id, key
}

func (f *fixture) bind(identityID, proxyID string) {
	f.t.Helper()
	f.exec(`INSERT INTO proxy_bindings (identity_id, proxy_id) VALUES ($1, $2)
	        ON CONFLICT (identity_id) DO UPDATE SET proxy_id = EXCLUDED.proxy_id`, identityID, proxyID)
}

// hgetall returns a hash as a map.
func (f *fixture) hgetall(key string) map[string]string {
	f.t.Helper()
	m, err := f.rdb.Do(f.ctx, f.rdb.B().Hgetall().Key(key).Build()).AsStrMap()
	require.NoError(f.t, err)
	return m
}

// zscore returns the score of member and whether it is present.
func (f *fixture) zscore(key string, member int64) (int64, bool) {
	f.t.Helper()
	v, err := f.rdb.Do(f.ctx, f.rdb.B().Zscore().Key(key).Member(strconv.FormatInt(member, 10)).Build()).AsFloat64()
	if rueidis.IsRedisNil(err) {
		return 0, false
	}
	require.NoError(f.t, err)
	return int64(v), true
}

func (f *fixture) do(cmd rueidis.Completed) {
	f.t.Helper()
	require.NoError(f.t, f.rdb.Do(f.ctx, cmd).Error())
}

func (f *fixture) smembers(key string) []string {
	f.t.Helper()
	v, err := f.rdb.Do(f.ctx, f.rdb.B().Smembers().Key(key).Build()).AsStrSlice()
	require.NoError(f.t, err)
	return v
}

func (f *fixture) exists(key string) bool {
	f.t.Helper()
	n, err := f.rdb.Do(f.ctx, f.rdb.B().Exists().Key(key).Build()).AsInt64()
	require.NoError(f.t, err)
	return n > 0
}

func randomBytes() []byte {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return b
}

func ms(t time.Time) string { return strconv.FormatInt(t.UnixMilli(), 10) }

func key(v int64) string { return strconv.FormatInt(v, 10) }

func groupKey(client, name string) catalog.GroupKey {
	return catalog.GroupKey{Client: client, Name: name}
}
