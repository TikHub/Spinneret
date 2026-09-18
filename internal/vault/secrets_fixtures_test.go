package vault_test

import (
	"context"
	"encoding/json"
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
	"github.com/TikHub/Spinneret/internal/pkg/idgen"
	"github.com/TikHub/Spinneret/internal/testutil"
	"github.com/TikHub/Spinneret/internal/vault"
	"github.com/TikHub/Spinneret/internal/vault/vaulttest"
)

// recorder captures audit entries and optionally inserts them into
// audit_logs synchronously so access-log queries can see them.
type recorder struct {
	mu      sync.Mutex
	entries []audit.Entry
	pool    *pgxpool.Pool
	t       testing.TB
}

func (r *recorder) Record(ctx context.Context, e audit.Entry) {
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	r.mu.Lock()
	r.entries = append(r.entries, e)
	r.mu.Unlock()
	if r.pool == nil {
		return
	}
	details, err := json.Marshal(e.Details)
	require.NoError(r.t, err)
	if e.Details == nil {
		details = []byte("{}")
	}
	_, err = r.pool.Exec(context.WithoutCancel(ctx), `
INSERT INTO audit_logs (id, created_at, tenant_id, namespace_id, actor_kind, actor_id, actor_name, action,
                        resource_kind, resource_id, resource_name, result, ip, user_agent, details)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
		idgen.New(idgen.Audit), e.CreatedAt, e.TenantID, e.NamespaceID, e.ActorKind, e.ActorID, e.ActorName, e.Action,
		e.ResourceKind, e.ResourceID, e.ResourceName, e.Result, e.IP, e.UserAgent, details)
	require.NoError(r.t, err)
}

func (r *recorder) byAction(action string) []audit.Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []audit.Entry
	for _, e := range r.entries {
		if e.Action == action {
			out = append(out, e)
		}
	}
	return out
}

func (r *recorder) last(t *testing.T) audit.Entry {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	require.NotEmpty(t, r.entries)
	return r.entries[len(r.entries)-1]
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}

// fixture is a migrated database with two tenants, each with a "prod"
// namespace, plus a "staging" namespace in the first tenant.
type fixture struct {
	pool    *pgxpool.Pool
	cipher  *vault.Cipher
	rec     *recorder
	store   *vault.SecretStore
	tenant  string
	ns      *catalog.Namespace
	staging *catalog.Namespace
	other   *catalog.Namespace // namespace of another tenant
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := testutil.Postgres(t)
	f := &fixture{pool: pool, cipher: vaulttest.NewCipher(t)}
	f.rec = &recorder{pool: pool, t: t}
	f.tenant = idgen.New(idgen.Tenant)
	f.ns = seedNamespace(t, pool, f.tenant, "prod", true)
	f.staging = seedNamespace(t, pool, f.tenant, "staging", false)
	f.other = seedNamespace(t, pool, idgen.New(idgen.Tenant), "prod", true)
	f.store = vault.NewSecretStore(pool, f.cipher, f.rec, nil)
	return f
}

func seedNamespace(t *testing.T, pool *pgxpool.Pool, tenantID, name string, createTenant bool) *catalog.Namespace {
	t.Helper()
	ctx := context.Background()
	if createTenant {
		_, err := pool.Exec(ctx, `INSERT INTO tenants (id, name) VALUES ($1, $2)`, tenantID, tenantID)
		require.NoError(t, err)
	}
	nsID := idgen.New(idgen.Namespace)
	_, err := pool.Exec(ctx, `INSERT INTO namespaces (id, tenant_id, name) VALUES ($1, $2, $3)`, nsID, tenantID, name)
	require.NoError(t, err)
	return catalogtest.NewNamespace(tenantID, nsID, name)
}

// Principals.

func userWith(tenantID string, role authz.Role, namespaceID string, extra ...authz.Permission) *authz.Principal {
	return &authz.Principal{
		Kind: authz.KindUser, ID: idgen.New(idgen.User), Name: "user-" + string(role), TenantID: tenantID,
		ClientIP: "10.0.0.1", UserAgent: "test",
		Bindings: []authz.Binding{{
			ID: idgen.New(idgen.RoleBinding), TenantID: tenantID, Role: role, NamespaceID: namespaceID, Extra: extra,
		}},
	}
}

func (f *fixture) admin() *authz.Principal    { return userWith(f.tenant, authz.RoleAdmin, "") }
func (f *fixture) viewer() *authz.Principal   { return userWith(f.tenant, authz.RoleViewer, "") }
func (f *fixture) operator() *authz.Principal { return userWith(f.tenant, authz.RoleOperator, "") }

// siteRestricted is a viewer pinned to one site of the namespace.
func (f *fixture) siteRestricted() *authz.Principal {
	p := userWith(f.tenant, authz.RoleAdmin, f.ns.ID)
	p.Bindings[0].SiteIDs = []string{idgen.New(idgen.Site)}
	return p
}

func (f *fixture) outsider() *authz.Principal {
	return userWith(f.other.TenantID, authz.RoleOwner, "")
}

func platformAdmin() *authz.Principal {
	return &authz.Principal{Kind: authz.KindUser, ID: idgen.New(idgen.User), Name: "root", IsPlatformAdmin: true}
}

func tokenFor(t *testing.T, ns *catalog.Namespace, scopes ...string) *authz.Principal {
	t.Helper()
	parsed, err := authz.ParseScopes(scopes)
	require.NoError(t, err)
	return &authz.Principal{
		Kind: authz.KindToken, ID: idgen.New(idgen.Token), Name: "node", TenantID: ns.TenantID,
		NamespaceID: ns.ID, NamespaceName: ns.Name, Scopes: parsed, ClientIP: "192.0.2.10", Node: "node-1",
	}
}

func (f *fixture) create(t *testing.T, path, value string) vault.Secret {
	t.Helper()
	s, err := f.store.Create(context.Background(), f.admin(), f.ns, vault.CreateSecretInput{Path: path, Value: value})
	require.NoError(t, err)
	return s
}

func requireReason(t *testing.T, err error, reason apperr.Reason) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, reason, apperr.ReasonOf(err), "error: %v", err)
}

func ptr[T any](v T) *T { return &v }
