package configcenter

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/audit"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/catalog/catalogtest"
	"github.com/TikHub/Spinneret/internal/events"
	"github.com/TikHub/Spinneret/internal/observability"
	"github.com/TikHub/Spinneret/internal/pkg/idgen"
	"github.com/TikHub/Spinneret/internal/testutil"
)

// fakeSecrets enforces the vault permission rules (tokens: secret:read,
// others: secret:reveal) over in-memory values.
type fakeSecrets struct {
	mu       sync.Mutex
	values   map[string]map[int]string // path → version → value; 0 = current
	purposes []string
	failWith error
}

func newFakeSecrets() *fakeSecrets {
	return &fakeSecrets{values: make(map[string]map[int]string)}
}

func (f *fakeSecrets) put(path string, version int, value string, current bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.values[path] == nil {
		f.values[path] = make(map[int]string)
	}
	f.values[path][version] = value
	if current {
		f.values[path][0] = value
	}
}

func (f *fakeSecrets) ReadSecret(_ context.Context, p *authz.Principal, ns *catalog.Namespace, path string, version int, purpose string) (SecretValue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.purposes = append(f.purposes, purpose)
	if f.failWith != nil {
		return SecretValue{}, f.failWith
	}
	perm := authz.PermSecretReveal
	if p.Kind == authz.KindToken {
		perm = authz.PermSecretRead
	}
	res := authz.Resource{TenantID: ns.TenantID, NamespaceID: ns.ID, NamespaceName: ns.Name, SecretPath: path}
	if err := p.Require(perm, res); err != nil {
		return SecretValue{}, err
	}
	v, ok := f.values[path][version]
	if !ok {
		return SecretValue{}, apperr.NotFound("secret %q not found", path)
	}
	return SecretValue{Path: path, Version: version, Value: v}, nil
}

func (f *fakeSecrets) purposeLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.purposes...)
}

// fakeRuntime serves runtime content with controllable versions.
type fakeRuntime struct {
	mu       sync.Mutex
	versions map[string]int64
	err      error
	contents int
}

func newFakeRuntime() *fakeRuntime { return &fakeRuntime{versions: make(map[string]int64)} }

func (f *fakeRuntime) bump(nsID, kind string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.versions[nsID+"/"+kind]++
	return f.versions[nsID+"/"+kind]
}

// set forces the provider version of kind (for example after a Redis reset).
func (f *fakeRuntime) set(nsID, kind string, v int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.versions[nsID+"/"+kind] = v
}

func (f *fakeRuntime) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeRuntime) RuntimeVersion(_ context.Context, nsID, kind string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return 0, f.err
	}
	return f.versions[nsID+"/"+kind], nil
}

func (f *fakeRuntime) RuntimeContent(_ context.Context, nsID, kind string) (string, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return "", 0, f.err
	}
	f.contents++
	v := f.versions[nsID+"/"+kind]
	return fmt.Sprintf(`{"kind":%q,"version":%d}`, kind, v), v, nil
}

// auditLog records audit entries.
type auditLog struct {
	mu      sync.Mutex
	entries []audit.Entry
}

func (a *auditLog) Record(_ context.Context, e audit.Entry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, e)
}

func (a *auditLog) find(action, result string) []audit.Entry {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []audit.Entry
	for _, e := range a.entries {
		if e.Action == action && e.Result == result {
			out = append(out, e)
		}
	}
	return out
}

// eventLog records bus events.
type eventLog struct {
	mu     sync.Mutex
	events []recordedEvent
}

type recordedEvent struct {
	channel string
	ev      events.Event
}

func (l *eventLog) handler(_ context.Context, channel string, ev events.Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, recordedEvent{channel: channel, ev: ev})
}

func (l *eventLog) on(channel string) []events.Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []events.Event
	for _, r := range l.events {
		if r.channel == channel {
			out = append(out, r.ev)
		}
	}
	return out
}

type env struct {
	pool    *pgxpool.Pool
	svc     *Service
	cat     *catalogtest.Catalog
	bus     events.Bus
	ns      *catalog.Namespace
	otherNS *catalog.Namespace
	secrets *fakeSecrets
	runtime *fakeRuntime
	audit   *auditLog
	events  *eventLog
	metrics *observability.Metrics

	admin    *authz.Principal
	operator *authz.Principal
	viewer   *authz.Principal
}

func newEnv(t *testing.T, cfg Config) *env {
	t.Helper()
	pool := testutil.Postgres(t)
	e := &env{
		pool:    pool,
		bus:     events.NewMemoryBus(),
		secrets: newFakeSecrets(),
		runtime: newFakeRuntime(),
		audit:   &auditLog{},
		events:  &eventLog{},
		metrics: observability.NewMetrics(),
	}
	tenantID := idgen.New(idgen.Tenant)
	otherTenantID := idgen.New(idgen.Tenant)
	e.ns = catalogtest.NewNamespace(tenantID, idgen.New(idgen.Namespace), "prod")
	e.otherNS = catalogtest.NewNamespace(otherTenantID, idgen.New(idgen.Namespace), "prod")
	for _, ns := range []*catalog.Namespace{e.ns, e.otherNS} {
		e.exec(t, `INSERT INTO tenants (id, name) VALUES ($1, $2)`, ns.TenantID, "tenant-"+ns.TenantID)
		e.exec(t, `INSERT INTO namespaces (id, tenant_id, name) VALUES ($1, $2, $3)`, ns.ID, ns.TenantID, ns.Name)
	}
	e.cat = catalogtest.New(e.ns, e.otherNS)
	e.bus.Subscribe(events.ChannelAll, e.events.handler)
	e.svc = New(cfg, pool, e.cat, e.bus, e.secrets, e.runtime, e.audit, e.metrics, nil)
	e.admin = userPrincipal(tenantID, authz.RoleAdmin)
	e.operator = userPrincipal(tenantID, authz.RoleOperator)
	e.viewer = userPrincipal(tenantID, authz.RoleViewer)
	e.start(t)
	return e
}

// start runs the service loop and waits until its bus subscriptions work.
func (e *env) start(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.svc.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		require.NoError(t, <-done)
	})
	probe := itemKey{ns: "probe", group: "g", key: "k"}
	require.Eventually(t, func() bool {
		e.svc.hub.mu.Lock()
		before := e.svc.hub.seq
		e.svc.hub.mu.Unlock()
		data, _ := json.Marshal(ConfigEventData{NS: probe.ns, Group: probe.group, Key: probe.key})
		_ = e.bus.Publish(context.Background(), events.ChannelConfig, events.Event{Type: "probe", Data: data})
		e.svc.hub.mu.Lock()
		defer e.svc.hub.mu.Unlock()
		return e.svc.hub.seq > before
	}, 5*time.Second, 5*time.Millisecond)
}

func (e *env) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := e.pool.Exec(ctx, sql, args...)
	require.NoError(t, err)
}

// addSecret inserts a secret with versions 1..versions into the namespace.
func (e *env) addSecret(t *testing.T, ns *catalog.Namespace, path string, versions int, value string) {
	t.Helper()
	id := idgen.New(idgen.Secret)
	e.exec(t, `INSERT INTO secrets (id, namespace_id, path, current_version) VALUES ($1, $2, $3, $4)`, id, ns.ID, path, versions)
	for v := 1; v <= versions; v++ {
		e.exec(t, `INSERT INTO secret_versions (secret_id, version, ciphertext, wrapped_dek, kek_id) VALUES ($1, $2, '\x00', '\x00', 'k1')`, id, v)
		e.secrets.put(path, v, fmt.Sprintf("%s-v%d", value, v), v == versions)
	}
}

func (e *env) token(t *testing.T, ns *catalog.Namespace, scopes ...string) *authz.Principal {
	t.Helper()
	parsed, err := authz.ParseScopes(scopes)
	require.NoError(t, err)
	return &authz.Principal{
		Kind: authz.KindToken, ID: idgen.New(idgen.Token), Name: "node", TenantID: ns.TenantID,
		NamespaceID: ns.ID, NamespaceName: ns.Name, Scopes: parsed,
	}
}

func userPrincipal(tenantID string, role authz.Role, extra ...authz.Permission) *authz.Principal {
	return &authz.Principal{
		Kind: authz.KindUser, ID: idgen.New(idgen.User), Name: string(role), TenantID: tenantID,
		Bindings: []authz.Binding{{ID: idgen.New(idgen.RoleBinding), TenantID: tenantID, Role: role, Extra: extra}},
	}
}

// create creates and optionally publishes an item as admin.
func (e *env) create(t *testing.T, group, key, format, content string, publish bool) Item {
	t.Helper()
	it, err := e.svc.CreateItem(context.Background(), e.admin, e.ns, CreateRequest{
		Group: group, Key: key, Format: format, Content: content, Publish: publish,
	})
	require.NoError(t, err)
	return it
}

// publishContent saves a draft and publishes it as admin.
func (e *env) publishContent(t *testing.T, id, content string) int32 {
	t.Helper()
	ctx := context.Background()
	_, err := e.svc.SaveDraft(ctx, e.admin, DraftRequest{ID: id, Content: content})
	require.NoError(t, err)
	_, v, err := e.svc.Publish(ctx, e.admin, PublishRequest{ID: id})
	require.NoError(t, err)
	return v
}

func requireReason(t *testing.T, err error, reason apperr.Reason) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, reason, apperr.ReasonOf(err), "error: %v", err)
}
