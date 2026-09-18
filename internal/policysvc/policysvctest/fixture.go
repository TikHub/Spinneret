// Package policysvctest provides an integration fixture for tests of the
// policy administration service and its Connect handler: a fresh migrated
// PostgreSQL database holding a tenant, a namespace, sites and endpoint groups
// that mirror an in-memory catalog snapshot, the built-in default policies,
// recording fakes for the hot-state syncer, audit recorder and event bus, and
// principals with typical role bindings.
package policysvctest

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/audit"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/catalog/catalogtest"
	"github.com/TikHub/Spinneret/internal/events"
	"github.com/TikHub/Spinneret/internal/pkg/idgen"
	"github.com/TikHub/Spinneret/internal/policysvc"
	"github.com/TikHub/Spinneret/internal/site"
	"github.com/TikHub/Spinneret/internal/testutil"
)

// Names used by the fixture.
const (
	NamespaceName = "crawl"
	SiteShop      = "shop"
	SiteForum     = "forum"
	GroupSearch   = "search"
	SearchPrefix  = "/api/v1/search"
)

// HotRecorder records SyncSite calls.
type HotRecorder struct {
	mu    sync.Mutex
	sites []string
	err   error
}

// SyncSite implements policysvc.HotSyncer.
func (h *HotRecorder) SyncSite(_ context.Context, siteID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sites = append(h.sites, siteID)
	return h.err
}

// SetError makes subsequent SyncSite calls fail with err.
func (h *HotRecorder) SetError(err error) {
	h.mu.Lock()
	h.err = err
	h.mu.Unlock()
}

// Take returns the recorded site IDs (sorted) and clears them.
func (h *HotRecorder) Take() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := h.sites
	h.sites = nil
	slices.Sort(out)
	return out
}

// AuditRecorder records audit entries.
type AuditRecorder struct {
	mu      sync.Mutex
	entries []audit.Entry
}

// Record implements audit.Recorder.
func (a *AuditRecorder) Record(_ context.Context, e audit.Entry) {
	a.mu.Lock()
	a.entries = append(a.entries, e)
	a.mu.Unlock()
}

// Take returns the recorded entries and clears them.
func (a *AuditRecorder) Take() []audit.Entry {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.entries
	a.entries = nil
	return out
}

// EventRecorder collects events published on a bus.
type EventRecorder struct {
	mu     sync.Mutex
	events []events.Event
}

// Take returns the recorded events and clears them.
func (r *EventRecorder) Take() []events.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.events
	r.events = nil
	return out
}

// Fixture is the integration test environment.
type Fixture struct {
	Pool      *pgxpool.Pool
	Catalog   *catalogtest.Catalog
	Namespace *catalog.Namespace
	Shop      *catalog.Site
	Forum     *catalog.Site
	Search    *catalog.EndpointGroup
	Hot       *HotRecorder
	Audit     *AuditRecorder
	Events    *EventRecorder
	Service   *policysvc.Service

	// Principals.
	Admin          *authz.Principal // admin role on the tenant
	Operator       *authz.Principal // operator role on the tenant
	Viewer         *authz.Principal // viewer role on the tenant
	ShopAdmin      *authz.Principal // admin role narrowed to the shop site
	OtherTenant    *authz.Principal // admin of another tenant
	AdminToken     *authz.Principal // token with the admin scope
	LeaseToken     *authz.Principal // token with lease:acquire only
	OperatorPubExt *authz.Principal // operator with the policy:publish extra permission
}

// New builds the fixture. It skips the test in -short mode.
func New(t testing.TB) *Fixture {
	t.Helper()
	pool := testutil.Postgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	tenantID := idgen.New(idgen.Tenant)
	ns := catalogtest.NewNamespace(tenantID, idgen.New(idgen.Namespace), NamespaceName)
	shop := catalogtest.AddSite(ns, idgen.New(idgen.Site), SiteShop, 101, "web", "app")
	forum := catalogtest.AddSite(ns, idgen.New(idgen.Site), SiteForum, 102, "web")
	search := catalogtest.AddGroup(shop, idgen.New(idgen.EndpointGroup), "web", GroupSearch, 101_900,
		site.Rule{Kind: site.RulePrefix, Pattern: SearchPrefix})

	exec := func(sql string, args ...any) {
		t.Helper()
		_, err := pool.Exec(ctx, sql, args...)
		require.NoError(t, err)
	}
	exec(`INSERT INTO tenants (id, name) VALUES ($1, $2)`, tenantID, "t-"+tenantID[len(tenantID)-8:])
	exec(`INSERT INTO namespaces (id, tenant_id, name) VALUES ($1, $2, $3)`, ns.ID, tenantID, ns.Name)
	for _, s := range []*catalog.Site{shop, forum} {
		exec(`INSERT INTO sites (id, namespace_id, name, clients) VALUES ($1, $2, $3, $4)`, s.ID, ns.ID, s.Name, s.Clients)
		for _, g := range s.GroupsByID {
			exec(`INSERT INTO endpoint_groups (id, site_id, client, name) VALUES ($1, $2, $3, $4)`, g.ID, s.ID, g.Client, g.Name)
		}
	}

	cat := catalogtest.New(ns)
	f := &Fixture{
		Pool: pool, Catalog: cat, Namespace: ns, Shop: shop, Forum: forum, Search: search,
		Hot: &HotRecorder{}, Audit: &AuditRecorder{}, Events: &EventRecorder{},
	}
	bus := events.NewMemoryBus()
	bus.Subscribe(events.NamespaceChannel(ns.ID), func(_ context.Context, _ string, ev events.Event) {
		f.Events.mu.Lock()
		f.Events.events = append(f.Events.events, ev)
		f.Events.mu.Unlock()
	})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	f.Service = policysvc.NewService(pool, cat, f.Hot, f.Audit, logger, policysvc.WithEventBus(bus))

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, f.Service.InstallNamespaceDefaults(ctx, tx, ns.ID, "system"))
	require.NoError(t, tx.Commit(ctx))

	f.Admin = User("usr_admin", authz.Binding{TenantID: tenantID, Role: authz.RoleAdmin})
	f.Operator = User("usr_operator", authz.Binding{TenantID: tenantID, Role: authz.RoleOperator})
	f.Viewer = User("usr_viewer", authz.Binding{TenantID: tenantID, Role: authz.RoleViewer})
	f.ShopAdmin = User("usr_shop", authz.Binding{TenantID: tenantID, Role: authz.RoleAdmin, NamespaceID: ns.ID, SiteIDs: []string{shop.ID}})
	f.OtherTenant = User("usr_other", authz.Binding{TenantID: idgen.New(idgen.Tenant), Role: authz.RoleOwner})
	f.OperatorPubExt = User("usr_oppub", authz.Binding{
		TenantID: tenantID, Role: authz.RoleOperator, Extra: []authz.Permission{authz.PermPolicyPublish},
	})
	f.AdminToken = Token(t, "tok_admin", tenantID, ns, "admin")
	f.LeaseToken = Token(t, "tok_lease", tenantID, ns, "lease:acquire")
	return f
}

// User returns a user principal with the given bindings; its active tenant is
// the tenant of the first binding.
func User(id string, bindings ...authz.Binding) *authz.Principal {
	p := &authz.Principal{Kind: authz.KindUser, ID: id, Name: id, Bindings: bindings}
	if len(bindings) > 0 {
		p.TenantID = bindings[0].TenantID
	}
	return p
}

// Token returns a token principal bound to ns with the given scopes.
func Token(t testing.TB, id, tenantID string, ns *catalog.Namespace, scopes ...string) *authz.Principal {
	t.Helper()
	parsed, err := authz.ParseScopes(scopes)
	require.NoError(t, err)
	return &authz.Principal{
		Kind: authz.KindToken, ID: id, Name: id, TenantID: tenantID,
		NamespaceID: ns.ID, NamespaceName: ns.Name, Scopes: parsed,
	}
}

// Ctx returns a context carrying p.
func Ctx(p *authz.Principal) context.Context {
	return authz.WithPrincipal(context.Background(), p)
}

// Count returns the result of a single-integer SQL query.
func (f *Fixture) Count(t testing.TB, sql string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, f.Pool.QueryRow(context.Background(), sql, args...).Scan(&n))
	return n
}
