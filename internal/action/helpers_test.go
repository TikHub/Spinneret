package action

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/redis/rueidis"
	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/audit"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/catalog/catalogtest"
	"github.com/TikHub/Spinneret/internal/events"
	"github.com/TikHub/Spinneret/internal/identity"
	"github.com/TikHub/Spinneret/internal/store/redis"
	"github.com/TikHub/Spinneret/internal/testutil"
)

const (
	testTenant    = "ten_test"
	testNamespace = "ns_test"
	testNSName    = "prod"
	siteAID       = "sit_a"
	siteBID       = "sit_b"
	siteAKey      = 11
	siteBKey      = 12
	typeWebID     = "ity_web"
	typeAppID     = "ity_app"
	typeBID       = "ity_b"
)

const typeYAML = `
name: %s
site: %s
client: %s
fields:
  token: { type: string, required: true, sensitive: true }
deliver:
  headers: { Authorization: "{{ token }}" }
`

// env is a test environment: a catalog namespace with two sites, a Redis key
// space and (optionally) a migrated PostgreSQL database with matching rows.
type env struct {
	t    *testing.T
	ctx  context.Context
	pool *pgxpool.Pool
	rdb  rueidis.Client
	keys redis.Keys
	cat  *catalogtest.Catalog
	ns   *catalog.Namespace

	siteA, siteB   *catalog.Site
	defWeb, defApp *catalog.EndpointGroup // site A defaults
	search         *catalog.EndpointGroup // site A web "search"
	defB           *catalog.EndpointGroup // site B web default

	hot   *fakeHot
	bus   events.Bus
	evts  *eventRecorder
	audit *auditRecorder
}

func newEnv(t *testing.T, withPG bool) *env {
	t.Helper()
	rdb, keys := testutil.Redis(t)
	e := &env{t: t, ctx: context.Background(), rdb: rdb, keys: keys, hot: &fakeHot{}, audit: &auditRecorder{}}
	e.ns = catalogtest.NewNamespace(testTenant, testNamespace, testNSName)
	e.siteA = catalogtest.AddSite(e.ns, siteAID, "site-a", siteAKey, "web", "app")
	e.siteB = catalogtest.AddSite(e.ns, siteBID, "site-b", siteBKey, "web")
	e.search = catalogtest.AddGroup(e.siteA, "eg_search", "web", "search", 11500)
	catalogtest.AddIdentityType(e.siteA, compileType(typeWebID, siteAID, "site-a", "web"))
	catalogtest.AddIdentityType(e.siteA, compileType(typeAppID, siteAID, "site-a", "app"))
	catalogtest.AddIdentityType(e.siteB, compileType(typeBID, siteBID, "site-b", "web"))
	e.defWeb, _ = e.siteA.Group("web", "_default")
	e.defApp, _ = e.siteA.Group("app", "_default")
	e.defB, _ = e.siteB.Group("web", "_default")
	e.cat = catalogtest.New(e.ns)
	e.bus = events.NewMemoryBus()
	e.evts = &eventRecorder{}
	e.bus.Subscribe(events.ChannelAll, e.evts.handle)
	if withPG {
		e.pool = testutil.Postgres(t)
		e.seedPG()
	}
	return e
}

func compileType(id, siteID, siteName, client string) *identity.CompiledType {
	return catalogtest.MustCompileType(id, siteID, 1, fmt.Sprintf(typeYAML, id+"_t", siteName, client))
}

func (e *env) seedPG() {
	e.exec(`INSERT INTO tenants (id, name) VALUES ($1, 'test')`, testTenant)
	e.exec(`INSERT INTO namespaces (id, tenant_id, name) VALUES ($1, $2, $3)`, testNamespace, testTenant, testNSName)
	e.exec(`INSERT INTO sites (id, hkey, namespace_id, name, clients) OVERRIDING SYSTEM VALUE VALUES ($1, $2, $3, 'site-a', '{web,app}')`, siteAID, siteAKey, testNamespace)
	e.exec(`INSERT INTO sites (id, hkey, namespace_id, name, clients) OVERRIDING SYSTEM VALUE VALUES ($1, $2, $3, 'site-b', '{web}')`, siteBID, siteBKey, testNamespace)
	for _, ty := range [][3]string{{typeWebID, siteAID, "web"}, {typeAppID, siteAID, "app"}, {typeBID, siteBID, "web"}} {
		e.exec(`INSERT INTO identity_types (id, site_id, client, name) VALUES ($1, $2, $3, $1)`, ty[0], ty[1], ty[2])
	}
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	_, err := e.pool.Exec(e.ctx, sql, args...)
	require.NoError(e.t, err)
}

// identitySeed describes a test identity.
type identitySeed struct {
	ID         string
	Key        int64
	Site       *catalog.Site
	Client     string
	TypeID     string
	State      string
	AccountID  string
	AccountKey int64
	BanUntil   *time.Time
	QuarUntil  *time.Time
	ChangedAt  time.Time
	NoRedis    bool
	NoPG       bool
}

func (e *env) addIdentity(s identitySeed) identitySeed {
	e.t.Helper()
	if s.Site == nil {
		s.Site = e.siteA
	}
	if s.Client == "" {
		s.Client = "web"
	}
	if s.TypeID == "" {
		s.TypeID = map[string]string{siteAID + "web": typeWebID, siteAID + "app": typeAppID, siteBID + "web": typeBID}[s.Site.ID+s.Client]
	}
	if s.State == "" {
		s.State = StateActive
	}
	if s.ChangedAt.IsZero() {
		s.ChangedAt = time.Now().Add(-time.Hour)
	}
	if e.pool != nil && !s.NoPG {
		var acc *string
		if s.AccountID != "" {
			acc = &s.AccountID
		}
		e.exec(`INSERT INTO identities (id, hkey, site_id, client, type_id, account_id, state, state_changed_at, ban_until, quarantine_until, unique_hash, payload_hash)
			OVERRIDING SYSTEM VALUE VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $11)`,
			s.ID, s.Key, s.Site.ID, s.Client, s.TypeID, acc, s.State, s.ChangedAt, s.BanUntil, s.QuarUntil, []byte(s.ID))
	}
	if !s.NoRedis {
		k := strconv.FormatInt(s.Key, 10)
		fields := []string{"iid", s.ID, "st", s.State, "acc", "", "bu", "0", "qu", "0"}
		if s.AccountKey > 0 {
			fields[5] = strconv.FormatInt(s.AccountKey, 10)
			e.do(e.rdb.B().Sadd().Key(e.keys.AccountMembers(s.Site.Key, s.AccountKey)).Member(k).Build())
		}
		if s.BanUntil != nil {
			fields[7] = strconv.FormatInt(s.BanUntil.UnixMilli(), 10)
		}
		e.do(e.rdb.B().Hset().Key(e.keys.Identity(s.Site.Key, s.Key)).FieldValue().FieldValue(fields[0], fields[1]).
			FieldValue(fields[2], fields[3]).FieldValue(fields[4], fields[5]).FieldValue(fields[6], fields[7]).FieldValue(fields[8], fields[9]).Build())
		if schedulable(s.State) {
			for _, eg := range eligibleGroupKeys(s.Site, s.Client, s.TypeID) {
				e.do(e.rdb.B().Zadd().Key(e.keys.Ready(s.Site.Key, eg)).ScoreMember().ScoreMember(0, k).Build())
			}
		}
	}
	return s
}

func (e *env) addAccount(id string, key int64, s *catalog.Site, state string) {
	e.t.Helper()
	if e.pool != nil {
		e.exec(`INSERT INTO accounts (id, hkey, site_id, external_ref, state, updated_at) OVERRIDING SYSTEM VALUE VALUES ($1, $2, $3, $1, $4, now() - interval '1 hour')`, id, key, s.ID, state)
	}
	e.do(e.rdb.B().Hset().Key(e.keys.Account(s.Key, key)).FieldValue().FieldValue("st", state).FieldValue("bu", "0").FieldValue("cd", "0").Build())
}

func (e *env) addProxy(id string, key int64, state string) {
	e.t.Helper()
	if e.pool != nil {
		e.exec(`INSERT INTO proxies (id, hkey, namespace_id, scheme, host, port, display_url, url_hash, url_ciphertext, url_wrapped_dek, url_kek_id, state, state_changed_at)
			OVERRIDING SYSTEM VALUE VALUES ($1, $2, $3, 'http', 'p.example', 8080, 'http://p.example:8080', $4, '\x00', '\x00', 'k1', $5, now() - interval '1 hour')`,
			id, key, testNamespace, []byte(id), state)
	}
	for _, s := range []*catalog.Site{e.siteA, e.siteB} {
		e.do(e.rdb.B().Hset().Key(e.keys.ProxySite(s.Key, key)).FieldValue().FieldValue("pid", id).FieldValue("st", state).FieldValue("cd", "0").FieldValue("gcd", "0").Build())
		if state == StateActive {
			e.do(e.rdb.B().Zadd().Key(e.keys.ProxyReady(s.Key)).ScoreMember().ScoreMember(0, strconv.FormatInt(key, 10)).Build())
		}
	}
}

func (e *env) do(cmd rueidis.Completed) rueidis.RedisResult {
	e.t.Helper()
	r := e.rdb.Do(e.ctx, cmd)
	require.NoError(e.t, ignoreNil(r.Error()))
	return r
}

func ignoreNil(err error) error {
	if rueidis.IsRedisNil(err) {
		return nil
	}
	return err
}

func (e *env) hget(key, field string) string {
	e.t.Helper()
	v, err := e.rdb.Do(e.ctx, e.rdb.B().Hget().Key(key).Field(field).Build()).ToString()
	require.NoError(e.t, ignoreNil(err))
	return v
}

// zscore returns the score of member in key and whether it is present.
func (e *env) zscore(key string, member int64) (float64, bool) {
	e.t.Helper()
	v, err := e.rdb.Do(e.ctx, e.rdb.B().Zscore().Key(key).Member(strconv.FormatInt(member, 10)).Build()).AsFloat64()
	if rueidis.IsRedisNil(err) {
		return 0, false
	}
	require.NoError(e.t, err)
	return v, true
}

func (e *env) zmembers(key string) []string {
	e.t.Helper()
	v, err := e.rdb.Do(e.ctx, e.rdb.B().Zrange().Key(key).Min("0").Max("-1").Build()).AsStrSlice()
	require.NoError(e.t, err)
	return v
}

func (e *env) smembers(key string) []string {
	e.t.Helper()
	v, err := e.rdb.Do(e.ctx, e.rdb.B().Smembers().Key(key).Build()).AsStrSlice()
	require.NoError(e.t, err)
	return v
}

func (e *env) pttl(key string) int64 {
	e.t.Helper()
	v, err := e.rdb.Do(e.ctx, e.rdb.B().Pttl().Key(key).Build()).AsInt64()
	require.NoError(e.t, err)
	return v
}

// identityState reads state columns of an identity row.
func (e *env) identityState(id string) (state, reason string, banUntil, quarUntil *time.Time) {
	e.t.Helper()
	err := e.pool.QueryRow(e.ctx, `SELECT state, state_reason, ban_until, quarantine_until FROM identities WHERE id = $1`, id).
		Scan(&state, &reason, &banUntil, &quarUntil)
	require.NoError(e.t, err)
	return
}

// stateEventRow is a subset of state_events used in assertions.
type stateEventRow struct {
	SubjectKind, SubjectID, From, To, Action, Scope, Actor, Reason, Rule, EG string
	Until                                                                    *time.Time
	Permanent, Shadow                                                        bool
}

func (e *env) stateEvents(subjectID string) []stateEventRow {
	e.t.Helper()
	rows, err := e.pool.Query(e.ctx, `SELECT subject_kind, subject_id, from_state, to_state, action, scope, actor, reason, rule, endpoint_group_id, until, permanent, shadow
		FROM state_events WHERE subject_id = $1 ORDER BY created_at, id`, subjectID)
	require.NoError(e.t, err)
	defer rows.Close()
	var out []stateEventRow
	for rows.Next() {
		var r stateEventRow
		require.NoError(e.t, rows.Scan(&r.SubjectKind, &r.SubjectID, &r.From, &r.To, &r.Action, &r.Scope, &r.Actor, &r.Reason, &r.Rule, &r.EG, &r.Until, &r.Permanent, &r.Shadow))
		out = append(out, r)
	}
	require.NoError(e.t, rows.Err())
	return out
}

func (e *env) operator() *Operator {
	return NewOperator(e.pool, e.rdb, e.keys, e.cat, e.hot, NewStateWriter(e.pool, nil, nil), e.audit, e.bus, nil)
}

// Principals.
func operatorUser(siteIDs ...string) *authz.Principal {
	return &authz.Principal{Kind: authz.KindUser, ID: "usr_op", TenantID: testTenant,
		Bindings: []authz.Binding{{TenantID: testTenant, Role: authz.RoleOperator, SiteIDs: siteIDs}}}
}

func viewerUser() *authz.Principal {
	return &authz.Principal{Kind: authz.KindUser, ID: "usr_view", TenantID: testTenant,
		Bindings: []authz.Binding{{TenantID: testTenant, Role: authz.RoleViewer}}}
}

func tokenPrincipal(t *testing.T, scopes ...string) *authz.Principal {
	t.Helper()
	parsed, err := authz.ParseScopes(scopes)
	require.NoError(t, err)
	return &authz.Principal{Kind: authz.KindToken, ID: "tok_1", TenantID: testTenant, NamespaceID: testNamespace, NamespaceName: testNSName, Scopes: parsed}
}

// fakeHot records hot sync calls.
type fakeHot struct {
	mu         sync.Mutex
	identities []hotCall
	accounts   []hotCall
	proxies    []hotCall
	err        error
}

type hotCall struct {
	Scope string
	IDs   []string
	Opts  SyncOptions
}

func (f *fakeHot) SyncIdentities(_ context.Context, siteID string, ids []string, opts SyncOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.identities = append(f.identities, hotCall{Scope: siteID, IDs: append([]string(nil), ids...), Opts: opts})
	return f.err
}

func (f *fakeHot) SyncAccounts(_ context.Context, siteID string, ids []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.accounts = append(f.accounts, hotCall{Scope: siteID, IDs: append([]string(nil), ids...)})
	return f.err
}

func (f *fakeHot) SyncProxies(_ context.Context, nsID string, ids []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.proxies = append(f.proxies, hotCall{Scope: nsID, IDs: append([]string(nil), ids...)})
	return f.err
}

func (f *fakeHot) syncedIdentities() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.identities {
		out = append(out, c.IDs...)
	}
	return out
}

// eventRecorder collects bus events.
type eventRecorder struct {
	mu     sync.Mutex
	events []events.Event
}

func (r *eventRecorder) handle(_ context.Context, _ string, ev events.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *eventRecorder) all() []events.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]events.Event(nil), r.events...)
}

// auditRecorder collects audit entries.
type auditRecorder struct {
	mu      sync.Mutex
	entries []audit.Entry
}

func (r *auditRecorder) Record(_ context.Context, e audit.Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, e)
}

func (r *auditRecorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = nil
}

func (r *auditRecorder) all() []audit.Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]audit.Entry(nil), r.entries...)
}

// counterValue reads the current value of a Prometheus counter.
func counterValue(c prometheus.Counter) float64 {
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		return -1
	}
	return m.GetCounter().GetValue()
}
