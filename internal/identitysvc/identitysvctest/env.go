package identitysvctest

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/catalog/catalogtest"
	"github.com/Evil0ctal/Spinneret/internal/events"
	"github.com/Evil0ctal/Spinneret/internal/identity"
	"github.com/Evil0ctal/Spinneret/internal/identitysvc"
	"github.com/Evil0ctal/Spinneret/internal/pkg/idgen"
	"github.com/Evil0ctal/Spinneret/internal/testutil"
	"github.com/Evil0ctal/Spinneret/internal/vault"
	"github.com/Evil0ctal/Spinneret/internal/vault/vaulttest"
)

// Identity type specs used by tests.
const (
	// WebCookieYAML is a probe-activated cookie type of client web.
	WebCookieYAML = `name: web_cookie
site: shop
client: web
fields:
  cookies:    { type: cookie_map, required: true, sensitive: true }
  user_agent: { type: string }
  signature:   { type: string, sensitive: true }
  api_key:    { type: secret_ref }
unique_by: [cookies.sessionid]
activation: probe
deliver:
  cookies: "{{ cookies }}"
  cookie_header: "{{ cookies }}"
  headers:
    User-Agent: "{{ user_agent }}"
    X-Api-Key: "{{ api_key }}"
  values:
    signature: "{{ signature }}"
`
	// AppDeviceYAML is an immediately activated device type of client app.
	AppDeviceYAML = `name: app_device
site: shop
client: app
fields:
  device_id:  { type: string, required: true }
  install_id: { type: string, required: true }
  extra:      { type: json }
unique_by: [device_id]
activation: immediate
deliver:
  query:
    device_id: "{{ device_id }}"
    iid: "{{ install_id }}"
  json: "{{ extra }}"
`
)

// Env is a migrated database with two tenants, a catalog snapshot matching
// its rows, recording fakes and a Service wired to them.
//
// Tenant (TenantID) namespace NS ("default") has sites SiteA ("shop",
// clients web and app) and SiteB ("market", client web). OtherTenantID has
// namespace OtherNS ("other") with site OtherSite ("shop", client web).
type Env struct {
	Pool    *pgxpool.Pool
	Cipher  *vault.Cipher
	Pepper  []byte
	Catalog *catalogtest.Catalog
	Bus     events.Bus
	Hot     *Hot
	Ops     *Operator
	Audit   *Audit
	Events  *Events
	Service *identitysvc.Service

	TenantID      string
	NS            *catalog.Namespace
	SiteA, SiteB  *catalog.Site
	OtherTenantID string
	OtherNS       *catalog.Namespace
	OtherSite     *catalog.Site
}

// NewEnv creates an environment backed by a fresh test database.
func NewEnv(t testing.TB) *Env {
	t.Helper()
	pool := testutil.Postgres(t)
	e := &Env{
		Pool:    pool,
		Cipher:  vaulttest.NewCipher(t),
		Pepper:  make([]byte, 32),
		Bus:     events.NewMemoryBus(),
		Hot:     &Hot{},
		Ops:     &Operator{},
		Audit:   &Audit{},
		Events:  &Events{},
		Catalog: catalogtest.New(),
	}
	if _, err := rand.Read(e.Pepper); err != nil {
		t.Fatalf("identitysvctest: pepper: %v", err)
	}
	t.Cleanup(e.Events.Subscribe(e.Bus))

	e.TenantID = e.insertTenant(t, "acme")
	e.NS = catalogtest.NewNamespace(e.TenantID, e.insertNamespace(t, e.TenantID, "default"), "default")
	e.SiteA = e.addSite(t, e.NS, "shop", "web", "app")
	e.SiteB = e.addSite(t, e.NS, "market", "web")
	e.OtherTenantID = e.insertTenant(t, "globex")
	e.OtherNS = catalogtest.NewNamespace(e.OtherTenantID, e.insertNamespace(t, e.OtherTenantID, "other"), "other")
	e.OtherSite = e.addSite(t, e.OtherNS, "shop", "web")
	e.Catalog.Put(e.NS)
	e.Catalog.Put(e.OtherNS)

	e.Service = identitysvc.NewService(pool, e.Cipher, e.Pepper, e.Catalog, e.Hot, e.Ops, e.Audit, e.Bus, nil)
	return e
}

func (e *Env) insertTenant(t testing.TB, name string) string {
	t.Helper()
	id := idgen.New(idgen.Tenant)
	e.Exec(t, `INSERT INTO tenants (id, name) VALUES ($1, $2)`, id, name)
	return id
}

func (e *Env) insertNamespace(t testing.TB, tenantID, name string) string {
	t.Helper()
	id := idgen.New(idgen.Namespace)
	e.Exec(t, `INSERT INTO namespaces (id, tenant_id, name) VALUES ($1, $2, $3)`, id, tenantID, name)
	return id
}

func (e *Env) addSite(t testing.TB, ns *catalog.Namespace, name string, clients ...string) *catalog.Site {
	t.Helper()
	id := idgen.New(idgen.Site)
	var key int64
	err := e.Pool.QueryRow(context.Background(),
		`INSERT INTO sites (id, namespace_id, name, clients) VALUES ($1, $2, $3, $4) RETURNING hkey`,
		id, ns.ID, name, clients).Scan(&key)
	if err != nil {
		t.Fatalf("identitysvctest: insert site: %v", err)
	}
	return catalogtest.AddSite(ns, id, name, key, clients...)
}

// Exec runs a statement and fails the test on error.
func (e *Env) Exec(t testing.TB, sql string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := e.Pool.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("identitysvctest: exec %q: %v", sql, err)
	}
}

// QueryInt runs a query returning one integer.
func (e *Env) QueryInt(t testing.TB, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := e.Pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("identitysvctest: query %q: %v", sql, err)
	}
	return n
}

// CreateType creates an identity type as the owner and registers its
// compiled form in the catalog snapshot (the fake catalog does not reload).
func (e *Env) CreateType(t testing.TB, site *catalog.Site, specYAML string) identitysvc.IdentityType {
	t.Helper()
	ns := e.namespaceOf(site)
	it, err := e.Service.CreateIdentityType(context.Background(), e.Owner(ns), ns, site.Name, specYAML)
	if err != nil {
		t.Fatalf("identitysvctest: create identity type: %v", err)
	}
	e.RegisterType(t, site, it)
	return it
}

// RegisterType (re)registers the compiled form of an identity type in the
// catalog snapshot of its site.
func (e *Env) RegisterType(t testing.TB, site *catalog.Site, it identitysvc.IdentityType) *identity.CompiledType {
	t.Helper()
	ct, err := identity.Compile(it.ID, site.ID, it.Version, it.Spec)
	if err != nil {
		t.Fatalf("identitysvctest: compile identity type: %v", err)
	}
	if _, ok := site.IdentityTypesByID[it.ID]; ok {
		site.IdentityTypes[ct.Name] = ct
		site.IdentityTypesByID[ct.ID] = ct
		return ct
	}
	catalogtest.AddIdentityType(site, ct)
	return ct
}

func (e *Env) namespaceOf(site *catalog.Site) *catalog.Namespace {
	if site.NamespaceID == e.OtherNS.ID {
		return e.OtherNS
	}
	return e.NS
}

// User returns a user principal with the given bindings, active in tenantID.
func User(tenantID string, bindings ...authz.Binding) *authz.Principal {
	return &authz.Principal{Kind: authz.KindUser, ID: idgen.New(idgen.User), Name: "user", TenantID: tenantID, Bindings: bindings}
}

// Owner returns a user holding the owner role in the tenant of ns.
func (e *Env) Owner(ns *catalog.Namespace) *authz.Principal {
	return User(ns.TenantID, authz.Binding{TenantID: ns.TenantID, Role: authz.RoleOwner})
}

// Role returns a user holding role on the whole default namespace, plus extra permissions.
func (e *Env) Role(role authz.Role, extra ...authz.Permission) *authz.Principal {
	return User(e.TenantID, authz.Binding{TenantID: e.TenantID, NamespaceID: e.NS.ID, Role: role, Extra: extra})
}

// SiteRole returns a user holding role on the given sites of the default namespace.
func (e *Env) SiteRole(role authz.Role, sites ...*catalog.Site) *authz.Principal {
	ids := make([]string, len(sites))
	for i, s := range sites {
		ids[i] = s.ID
	}
	return User(e.TenantID, authz.Binding{TenantID: e.TenantID, NamespaceID: e.NS.ID, Role: role, SiteIDs: ids})
}

// NoRole returns a user without bindings, active in the default tenant.
func (e *Env) NoRole() *authz.Principal {
	return User(e.TenantID)
}

// Stranger returns the owner of the other tenant, active there.
func (e *Env) Stranger() *authz.Principal {
	return e.Owner(e.OtherNS)
}

// Token returns an API token principal of the default namespace.
func (e *Env) Token(t testing.TB, scopes ...string) *authz.Principal {
	t.Helper()
	parsed, err := authz.ParseScopes(scopes)
	if err != nil {
		t.Fatalf("identitysvctest: parse scopes: %v", err)
	}
	return &authz.Principal{
		Kind: authz.KindToken, ID: idgen.New(idgen.Token), Name: "token", TenantID: e.TenantID,
		NamespaceID: e.NS.ID, NamespaceName: e.NS.Name, Scopes: parsed,
	}
}
