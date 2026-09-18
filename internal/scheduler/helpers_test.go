package scheduler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/redis/rueidis"
	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/catalog/catalogtest"
	"github.com/Evil0ctal/Spinneret/internal/identity"
	"github.com/Evil0ctal/Spinneret/internal/observability"
	"github.com/Evil0ctal/Spinneret/internal/policy"
	"github.com/Evil0ctal/Spinneret/internal/site"
	"github.com/Evil0ctal/Spinneret/internal/store/redis"
	"github.com/Evil0ctal/Spinneret/internal/testutil"
)

const (
	testSiteKey   = int64(7)
	testWebGroup  = int64(7000) // "_default" of client web (catalogtest: key*1000+index)
	testSearchKey = int64(70)
	testOtherKey  = int64(71)
	testTypeName  = "demo_cookie"
	testShards    = 16
)

// testEpoch is a minute boundary so quota windows are predictable.
var testEpoch = time.UnixMilli(1_758_011_400_000)

const testTypeYAML = `
name: demo_cookie
site: demo
client: web
fields:
  cookies: { type: cookie_map, required: true, sensitive: true }
unique_by: [cookies.sessionid]
deliver:
  cookie_header: "{{ cookies }}"
`

// fixture is a scheduler wired to a real Redis with an in-memory catalog.
type fixture struct {
	t       testing.TB
	ctx     context.Context
	rdb     rueidis.Client
	keys    redis.Keys
	cat     *catalogtest.Catalog
	ns      *catalog.Namespace
	site    *catalog.Site
	web     *catalog.EndpointGroup
	search  *catalog.EndpointGroup
	other   *catalog.EndpointGroup
	svc     *Service
	creds   *fakeCreds
	proxies *fakeProxies
	rec     *memRecorder
	metrics *observability.Metrics

	mu       sync.Mutex
	now      time.Time
	randoms  []float64
	halfOpen []int64
	bindings []ProxyBinding
}

func newFixture(t testing.TB) *fixture {
	t.Helper()
	rdb, keys := testutil.Redis(t)
	ns := catalogtest.NewNamespace("ten_1", "ns_1", "prod")
	st := catalogtest.AddSite(ns, "sit_1", "demo", testSiteKey, "web", "app")
	search := catalogtest.AddGroup(st, "eg_search", "web", "search", testSearchKey,
		site.Rule{Kind: site.RulePrefix, Pattern: "/search/"})
	other := catalogtest.AddGroup(st, "eg_other", "web", "other", testOtherKey)
	catalogtest.AddIdentityType(st, catalogtest.MustCompileType("ity_1", st.ID, 1, testTypeYAML))
	web, _ := st.Group("web", site.DefaultGroup)

	f := &fixture{
		t: t, ctx: context.Background(), rdb: rdb, keys: keys,
		cat: catalogtest.New(ns), ns: ns, site: st, web: web, search: search, other: other,
		creds: &fakeCreds{fail: map[string]bool{}}, proxies: &fakeProxies{fail: map[string]bool{}},
		rec: &memRecorder{}, metrics: observability.NewMetrics(), now: testEpoch,
	}
	cfg := Config{
		ReportShards:     testShards,
		LateReportWindow: 10 * time.Minute,
		OnBreakerHalfOpen: func(siteKey, groupKey int64) {
			f.mu.Lock()
			f.halfOpen = append(f.halfOpen, groupKey)
			f.mu.Unlock()
		},
		OnProxyBound: func(b ProxyBinding) {
			f.mu.Lock()
			f.bindings = append(f.bindings, b)
			f.mu.Unlock()
		},
	}
	f.svc = New(cfg, rdb, keys, f.cat, f.creds, f.proxies, f.rec, f.metrics, slog.New(slog.NewTextHandler(io.Discard, nil)))
	f.svc.clock = f.clock
	f.svc.random = f.random
	return f
}

func (f *fixture) clock() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fixture) nowMs() int64 { return f.clock().UnixMilli() }

func (f *fixture) advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	f.mu.Unlock()
}

// random pops the queued random values and falls back to 0.5.
func (f *fixture) random() float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.randoms) == 0 {
		return 0.5
	}
	v := f.randoms[0]
	f.randoms = f.randoms[1:]
	return v
}

func (f *fixture) setRandoms(v ...float64) {
	f.mu.Lock()
	f.randoms = append([]float64(nil), v...)
	f.mu.Unlock()
}

// rotation returns a mutable copy of the default rotation installed on g.
func (f *fixture) rotation(g *catalog.EndpointGroup) *policy.RotationSpec {
	rot := policy.Default(policy.KindRotation).(*policy.RotationSpec)
	g.Rotation = rot
	return rot
}

// tokenCtx returns a context carrying a token principal with the scopes.
func (f *fixture) tokenCtx(scopes ...string) context.Context {
	if len(scopes) == 0 {
		scopes = []string{"lease:acquire"}
	}
	parsed, err := authz.ParseScopes(scopes)
	require.NoError(f.t, err)
	return authz.WithPrincipal(f.ctx, &authz.Principal{
		Kind: authz.KindToken, ID: "tok_1", Name: "crawler", TenantID: f.ns.TenantID,
		NamespaceID: f.ns.ID, NamespaceName: f.ns.Name, Scopes: parsed, Node: "node-1",
	})
}

func (f *fixture) do(cmd rueidis.Completed) rueidis.RedisResult {
	return f.rdb.Do(f.ctx, cmd)
}

func (f *fixture) hset(key string, kv ...string) {
	f.t.Helper()
	require.Zero(f.t, len(kv)%2, "hset needs field/value pairs")
	b := f.rdb.B().Hset().Key(key).FieldValue()
	for i := 0; i < len(kv); i += 2 {
		b = b.FieldValue(kv[i], kv[i+1])
	}
	require.NoError(f.t, f.do(b.Build()).Error())
}

func (f *fixture) hget(key, field string) string {
	v, err := f.do(f.rdb.B().Hget().Key(key).Field(field).Build()).ToString()
	if rueidis.IsRedisNil(err) {
		return ""
	}
	require.NoError(f.t, err)
	return v
}

func (f *fixture) hgetall(key string) map[string]string {
	m, err := f.do(f.rdb.B().Hgetall().Key(key).Build()).AsStrMap()
	require.NoError(f.t, err)
	return m
}

func (f *fixture) zadd(key string, score int64, member string) {
	f.t.Helper()
	require.NoError(f.t, f.do(f.rdb.B().Zadd().Key(key).ScoreMember().ScoreMember(float64(score), member).Build()).Error())
}

// zscore returns the score of member or -1 when it is absent.
func (f *fixture) zscore(key, member string) int64 {
	v, err := f.do(f.rdb.B().Zscore().Key(key).Member(member).Build()).AsFloat64()
	if rueidis.IsRedisNil(err) {
		return -1
	}
	require.NoError(f.t, err)
	return int64(v)
}

func (f *fixture) exists(key string) bool {
	n, err := f.do(f.rdb.B().Exists().Key(key).Build()).AsInt64()
	require.NoError(f.t, err)
	return n == 1
}

func (f *fixture) pttl(key string) int64 {
	n, err := f.do(f.rdb.B().Pttl().Key(key).Build()).AsInt64()
	require.NoError(f.t, err)
	return n
}

func (f *fixture) get(key string) string {
	v, err := f.do(f.rdb.B().Get().Key(key).Build()).ToString()
	if rueidis.IsRedisNil(err) {
		return ""
	}
	require.NoError(f.t, err)
	return v
}

func (f *fixture) sismember(key, member string) bool {
	ok, err := f.do(f.rdb.B().Sismember().Key(key).Member(member).Build()).AsBool()
	require.NoError(f.t, err)
	return ok
}

func key(n int64) string { return strconv.FormatInt(n, 10) }

// identity creates identity i (state active, type demo_cookie) and adds it to
// the ready sets of groups at score. kv overrides identity hash fields.
func (f *fixture) identity(i int64, score int64, groups []int64, kv ...string) {
	f.t.Helper()
	fields := []string{"iid", fmt.Sprintf("idt_%d", i), "st", "active", "ty", testTypeName, "tv", "1", "pv", "3", "al", "0", "acc", "", "rg", ""}
	f.hset(f.keys.Identity(testSiteKey, i), append(fields, kv...)...)
	for _, g := range groups {
		f.zadd(f.keys.Ready(testSiteKey, g), score, key(i))
	}
}

func (f *fixture) health(g, i int64, packed string) {
	f.hset(f.keys.Health(testSiteKey, g), key(i), packed)
}

// proxy creates proxy p (active datacenter, mc 1) available at score.
func (f *fixture) proxy(p int64, score int64, kv ...string) {
	f.t.Helper()
	fields := []string{"pid", fmt.Sprintf("pxy_%d", p), "st", "active", "kd", "datacenter", "rg", "", "pv", "", "tg", "", "mc", "1", "al", "0"}
	f.hset(f.keys.ProxySite(testSiteKey, p), append(fields, kv...)...)
	f.zadd(f.keys.ProxyReady(testSiteKey), score, key(p))
}

func (f *fixture) lease(id string) map[string]string {
	return f.hgetall(f.keys.Lease(testSiteKey, id))
}

func (f *fixture) idField(i int64, field string) string {
	return f.hget(f.keys.Identity(testSiteKey, i), field)
}

func (f *fixture) ready(g, i int64) int64 {
	return f.zscore(f.keys.Ready(testSiteKey, g), key(i))
}

func (f *fixture) acquire(ctx context.Context, req AcquireRequest) (*Grant, error) {
	if req.Site == "" {
		req.Site = "demo"
	}
	if req.Client == "" {
		req.Client = "web"
	}
	return f.svc.Acquire(ctx, req)
}

func (f *fixture) mustAcquire(req AcquireRequest) *Grant {
	f.t.Helper()
	g, err := f.acquire(f.tokenCtx(), req)
	require.NoError(f.t, err)
	return g
}

func (f *fixture) mustRelease(id string) {
	f.t.Helper()
	ok, err := f.svc.Release(f.tokenCtx(), id)
	require.NoError(f.t, err)
	require.True(f.t, ok)
}

// requireReason asserts an apperr reason.
func requireReason(t *testing.T, err error, reason apperr.Reason) {
	t.Helper()
	_ = requireAppErr(t, err, reason)
}

// requireAppErr asserts an apperr reason and returns the error.
func requireAppErr(t *testing.T, err error, reason apperr.Reason) *apperr.Error {
	t.Helper()
	require.Error(t, err)
	e, ok := apperr.As(err)
	require.True(t, ok, "not an apperr: %v", err)
	require.Equal(t, reason, e.Reason, "error: %v", err)
	return e
}

type fakeCreds struct {
	mu    sync.Mutex
	fail  map[string]bool
	calls int
	// onCall, when set, runs before a credential is rendered.
	onCall func(identityID string)
}

func (c *fakeCreds) Credential(_ context.Context, t *identity.CompiledType, namespaceID, identityID string, payloadVersion int) (*identity.Credential, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.onCall != nil {
		c.onCall(identityID)
	}
	if c.fail[identityID] {
		return nil, errors.New("payload unavailable")
	}
	return &identity.Credential{
		CookieHeader: "sid=" + identityID,
		Values:       map[string]any{"type": t.Name, "ns": namespaceID, "pv": payloadVersion},
	}, nil
}

type fakeProxies struct {
	mu   sync.Mutex
	fail map[string]bool
}

func (p *fakeProxies) Resolve(_ context.Context, namespaceID, proxyID, identityID, leaseID string) (*ProxyAssignment, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fail[proxyID] {
		return nil, errors.New("proxy url unavailable")
	}
	return &ProxyAssignment{ID: proxyID, URL: "http://proxy.invalid/" + proxyID, Kind: "datacenter", Region: namespaceID}, nil
}

type memRecorder struct {
	mu       sync.Mutex
	acquires []AcquireRecord
	ends     []LeaseEndRecord
}

func (r *memRecorder) RecordAcquire(a AcquireRecord) {
	r.mu.Lock()
	r.acquires = append(r.acquires, a)
	r.mu.Unlock()
}

func (r *memRecorder) RecordLeaseEnd(e LeaseEndRecord) {
	r.mu.Lock()
	r.ends = append(r.ends, e)
	r.mu.Unlock()
}

func (r *memRecorder) acquireResults() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.acquires))
	for i, a := range r.acquires {
		out[i] = a.Result
	}
	return out
}

func (r *memRecorder) endKinds() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.ends))
	for i, e := range r.ends {
		out[i] = e.Kind
	}
	return out
}

// userCtx returns a context carrying a console user bound to the tenant with role.
func userCtx(f *fixture, role authz.Role) context.Context {
	return authz.WithPrincipal(f.ctx, &authz.Principal{
		Kind: authz.KindUser, ID: "usr_1", Name: "alice", TenantID: f.ns.TenantID,
		Bindings: []authz.Binding{{ID: "rb_1", TenantID: f.ns.TenantID, Role: role}},
	})
}
