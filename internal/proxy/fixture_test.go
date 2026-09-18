package proxy

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/rueidis"
	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/audit"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/catalog/catalogtest"
	"github.com/TikHub/Spinneret/internal/events"
	"github.com/TikHub/Spinneret/internal/proxy/proxydb"
	"github.com/TikHub/Spinneret/internal/store/redis"
	"github.com/TikHub/Spinneret/internal/testutil"
	"github.com/TikHub/Spinneret/internal/vault"
	"github.com/TikHub/Spinneret/internal/vault/vaulttest"
)

// fakeHot records hot-state sync calls.
type fakeHot struct {
	mu      sync.Mutex
	synced  []string
	removed []string
	// err fails SyncProxies and RemoveProxies; removeErr fails RemoveProxies only.
	err       error
	removeErr error
	// failNamespace restricts err and removeErr to one namespace when set.
	failNamespace string
	// onRemove, when set, runs at the start of RemoveProxies.
	onRemove func(namespaceID string, ids []string)
}

func (f *fakeHot) failure(namespaceID string, errs ...error) error {
	if f.failNamespace != "" && f.failNamespace != namespaceID {
		return nil
	}
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeHot) SyncProxies(_ context.Context, namespaceID string, ids []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.synced = append(f.synced, ids...)
	return f.failure(namespaceID, f.err)
}

func (f *fakeHot) RemoveProxies(_ context.Context, namespaceID string, ids []string) error {
	f.mu.Lock()
	hook := f.onRemove
	f.mu.Unlock()
	if hook != nil {
		hook(namespaceID, ids)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, ids...)
	return f.failure(namespaceID, f.removeErr, f.err)
}

func (f *fakeHot) syncedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.synced)
}

func (f *fakeHot) removedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.removed)
}

func (f *fakeHot) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.synced, f.removed, f.err, f.removeErr, f.failNamespace, f.onRemove = nil, nil, nil, nil, "", nil
}

// setOnRemove installs the RemoveProxies hook.
func (f *fakeHot) setOnRemove(fn func(namespaceID string, ids []string)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onRemove = fn
}

// recAudit captures audit entries.
type recAudit struct {
	mu      sync.Mutex
	entries []audit.Entry
}

func (r *recAudit) Record(_ context.Context, e audit.Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, e)
}

func (r *recAudit) actions() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.entries))
	for i, e := range r.entries {
		out[i] = e.Action
	}
	return out
}

// eventLog captures bus events.
type eventLog struct {
	mu     sync.Mutex
	events []events.Event
}

func (l *eventLog) handler(_ context.Context, _ string, ev events.Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, ev)
}

func (l *eventLog) snapshot() []events.Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.events)
}

// testEnv is an integration fixture with PostgreSQL, Redis, a catalog with one
// namespace and two sites.
type testEnv struct {
	pool   *pgxpool.Pool
	rdb    rueidis.Client
	keys   redis.Keys
	cipher *vault.Cipher
	pepper []byte
	cat    *catalogtest.Catalog
	ns     *catalog.Namespace
	alpha  *catalog.Site
	beta   *catalog.Site
	other  *catalog.Namespace
	hot    *fakeHot
	audit  *recAudit
	bus    events.Bus
	events *eventLog
	svc    *Service
	logger *slog.Logger
}

const (
	testTenant      = "ten_test"
	testOtherTenant = "ten_other"
)

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	pool := testutil.Postgres(t)
	rdb, keys := testutil.Redis(t)
	ctx := context.Background()
	for _, stmt := range []string{
		`INSERT INTO tenants (id, name) VALUES ('ten_test', 'test'), ('ten_other', 'other')`,
		`INSERT INTO namespaces (id, tenant_id, name) VALUES ('ns_main', 'ten_test', 'main'), ('ns_side', 'ten_test', 'side'), ('ns_foreign', 'ten_other', 'main')`,
	} {
		_, err := pool.Exec(ctx, stmt)
		require.NoError(t, err)
	}
	ns := catalogtest.NewNamespace(testTenant, "ns_main", "main")
	alpha := catalogtest.AddSite(ns, "sit_alpha", "alpha", 101)
	beta := catalogtest.AddSite(ns, "sit_beta", "beta", 102)
	side := catalogtest.NewNamespace(testTenant, "ns_side", "side")
	catalogtest.AddSite(side, "sit_gamma", "gamma", 103)
	foreign := catalogtest.NewNamespace(testOtherTenant, "ns_foreign", "main")
	cat := catalogtest.New(ns, side, foreign)

	env := &testEnv{
		pool: pool, rdb: rdb, keys: keys, cipher: vaulttest.NewCipher(t), pepper: []byte("0123456789abcdef0123456789abcdef"),
		cat: cat, ns: ns, alpha: alpha, beta: beta, other: side, hot: &fakeHot{}, audit: &recAudit{},
		bus: events.NewMemoryBus(), events: &eventLog{},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	env.bus.Subscribe(events.ChannelAll, env.events.handler)
	env.svc = NewService(pool, env.cipher, env.pepper, rdb, keys, cat, env.hot, env.audit, env.bus, env.logger)
	return env
}

// Principals used by the tests.
func adminUser() *authz.Principal {
	return &authz.Principal{Kind: authz.KindUser, ID: "usr_admin", Name: "admin", TenantID: testTenant,
		Bindings: []authz.Binding{{TenantID: testTenant, Role: authz.RoleAdmin}}}
}

func viewerUser() *authz.Principal {
	return &authz.Principal{Kind: authz.KindUser, ID: "usr_viewer", TenantID: testTenant,
		Bindings: []authz.Binding{{TenantID: testTenant, Role: authz.RoleViewer}}}
}

func siteOperator(nsID string, siteIDs ...string) *authz.Principal {
	return &authz.Principal{Kind: authz.KindUser, ID: "usr_siteop", TenantID: testTenant,
		Bindings: []authz.Binding{{TenantID: testTenant, Role: authz.RoleOperator, NamespaceID: nsID, SiteIDs: siteIDs}}}
}

func foreignUser() *authz.Principal {
	return &authz.Principal{Kind: authz.KindUser, ID: "usr_foreign", TenantID: testOtherTenant,
		Bindings: []authz.Binding{{TenantID: testOtherTenant, Role: authz.RoleOwner}}}
}

func tokenPrincipal(t *testing.T, scopes ...string) *authz.Principal {
	t.Helper()
	parsed, err := authz.ParseScopes(scopes)
	require.NoError(t, err)
	return &authz.Principal{Kind: authz.KindToken, ID: "tok_1", TenantID: testTenant, NamespaceID: "ns_main",
		NamespaceName: "main", Scopes: parsed}
}

// importLines imports URL lines into ns and returns the proxies ordered like the input.
func (e *testEnv) importLines(t *testing.T, ns *catalog.Namespace, lines ...string) []*Proxy {
	t.Helper()
	res, err := e.svc.ImportProxies(context.Background(), adminUser(), ns, ImportRequest{
		Format: FormatLines, Data: strings.Join(lines, "\n"),
	})
	require.NoError(t, err)
	require.Empty(t, res.Failed)
	out := make([]*Proxy, 0, len(lines))
	for _, line := range lines {
		u, err := ParseProxyURL(strings.Fields(line)[0])
		require.NoError(t, err)
		out = append(out, e.proxyByDisplay(t, ns, u.DisplayURL()))
	}
	return out
}

func (e *testEnv) proxyByDisplay(t *testing.T, ns *catalog.Namespace, display string) *Proxy {
	t.Helper()
	var id string
	err := e.pool.QueryRow(context.Background(),
		`SELECT id FROM proxies WHERE namespace_id = $1 AND display_url = $2 ORDER BY created_at DESC LIMIT 1`, ns.ID, display).Scan(&id)
	require.NoError(t, err)
	return e.row(t, id)
}

func (e *testEnv) row(t *testing.T, id string) *Proxy {
	t.Helper()
	row, err := proxydb.New(e.pool).ProxyGet(context.Background(), id)
	require.NoError(t, err)
	ns, _ := e.cat.Namespace(row.NamespaceID)
	return newProxyView(row, ns)
}

func (e *testEnv) rawRow(t *testing.T, id string) proxydb.Proxy {
	t.Helper()
	row, err := proxydb.New(e.pool).ProxyGet(context.Background(), id)
	require.NoError(t, err)
	return row
}

// materialize creates a minimal "px" hash for a proxy on a site, as the
// hot-state syncer would.
func (e *testEnv) materialize(t *testing.T, site *catalog.Site, p *Proxy, fields ...string) {
	t.Helper()
	ctx := context.Background()
	args := append([]string{"pid", p.ID, "st", p.State, "mc", "1"}, fields...)
	cmd := e.rdb.B().Hset().Key(e.keys.ProxySite(site.Key, p.Key)).FieldValue()
	for i := 0; i+1 < len(args); i += 2 {
		cmd = cmd.FieldValue(args[i], args[i+1])
	}
	require.NoError(t, e.rdb.Do(ctx, cmd.Build()).Error())
}

func (e *testEnv) hget(t *testing.T, key, field string) string {
	t.Helper()
	v, err := e.rdb.Do(context.Background(), e.rdb.B().Hget().Key(key).Field(field).Build()).ToString()
	if rueidis.IsRedisNil(err) {
		return ""
	}
	require.NoError(t, err)
	return v
}

func (e *testEnv) dirty(t *testing.T, site *catalog.Site) []string {
	t.Helper()
	members, err := e.rdb.Do(context.Background(), e.rdb.B().Smembers().Key(e.keys.Dirty(site.Key)).Build()).AsStrSlice()
	require.NoError(t, err)
	slices.Sort(members)
	return members
}

type seRow struct {
	SiteID, SubjectID, From, To, Action, Scope, Actor, Reason string
	Until                                                     *time.Time
	Permanent                                                 bool
}

func (e *testEnv) stateEvents(t *testing.T, proxyID string) []seRow {
	t.Helper()
	rows, err := e.pool.Query(context.Background(), `SELECT site_id, subject_id, from_state, to_state, action, scope, actor, reason, until, permanent
		FROM state_events WHERE subject_kind = 'proxy' AND subject_id = $1 ORDER BY created_at, id`, proxyID)
	require.NoError(t, err)
	defer rows.Close()
	var out []seRow
	for rows.Next() {
		var r seRow
		require.NoError(t, rows.Scan(&r.SiteID, &r.SubjectID, &r.From, &r.To, &r.Action, &r.Scope, &r.Actor, &r.Reason, &r.Until, &r.Permanent))
		out = append(out, r)
	}
	require.NoError(t, rows.Err())
	return out
}

// fixedClock returns a clock function fixed at t.
func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

func requireReason(t *testing.T, err error, reason apperr.Reason) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, reason, apperr.ReasonOf(err), "error: %v", err)
}
