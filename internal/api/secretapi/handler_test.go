package secretapi_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	spinneretv1 "github.com/TikHub/Spinneret/gen/go/spinneret/v1"
	"github.com/TikHub/Spinneret/internal/api/secretapi"
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

// recorder captures audit entries and writes them to audit_logs so access
// log RPCs can read them back.
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
	details := []byte("{}")
	if e.Details != nil {
		b, err := json.Marshal(e.Details)
		require.NoError(r.t, err)
		details = b
	}
	_, err := r.pool.Exec(context.WithoutCancel(ctx), `
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

type env struct {
	pool    *pgxpool.Pool
	rec     *recorder
	store   *vault.SecretStore
	rw      *vault.Rewrapper
	cat     *catalogtest.Catalog
	h       *secretapi.Handler
	node    *secretapi.NodeHandler
	tenant  string
	ns      *catalog.Namespace
	staging *catalog.Namespace
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := testutil.Postgres(t)
	cipher := vaulttest.NewCipher(t)
	e := &env{pool: pool, rec: &recorder{pool: pool, t: t}, tenant: idgen.New(idgen.Tenant)}
	_, err := pool.Exec(context.Background(), `INSERT INTO tenants (id, name) VALUES ($1, $1)`, e.tenant)
	require.NoError(t, err)
	e.ns = seedNamespace(t, pool, e.tenant, "prod")
	e.staging = seedNamespace(t, pool, e.tenant, "staging")
	e.cat = catalogtest.New(e.ns, e.staging)
	e.store = vault.NewSecretStore(pool, cipher, e.rec, nil)
	e.rw = vault.NewRewrapper(pool, cipher, nil)
	t.Cleanup(e.rw.Close)
	e.h = secretapi.New(e.store, e.rw, e.cat, e.rec, nil)
	e.node = secretapi.NewNodeHandler(e.store, e.cat)
	return e
}

func seedNamespace(t *testing.T, pool *pgxpool.Pool, tenantID, name string) *catalog.Namespace {
	t.Helper()
	id := idgen.New(idgen.Namespace)
	_, err := pool.Exec(context.Background(), `INSERT INTO namespaces (id, tenant_id, name) VALUES ($1, $2, $3)`, id, tenantID, name)
	require.NoError(t, err)
	return catalogtest.NewNamespace(tenantID, id, name)
}

func user(tenantID string, role authz.Role, extra ...authz.Permission) *authz.Principal {
	return &authz.Principal{
		Kind: authz.KindUser, ID: idgen.New(idgen.User), Name: string(role), TenantID: tenantID, ClientIP: "10.1.1.1",
		Bindings: []authz.Binding{{ID: idgen.New(idgen.RoleBinding), TenantID: tenantID, Role: role, Extra: extra}},
	}
}

func platformAdmin() *authz.Principal {
	return &authz.Principal{Kind: authz.KindUser, ID: idgen.New(idgen.User), Name: "root", IsPlatformAdmin: true}
}

func token(t *testing.T, ns *catalog.Namespace, scopes ...string) *authz.Principal {
	t.Helper()
	parsed, err := authz.ParseScopes(scopes)
	require.NoError(t, err)
	return &authz.Principal{
		Kind: authz.KindToken, ID: idgen.New(idgen.Token), Name: "node", TenantID: ns.TenantID,
		NamespaceID: ns.ID, NamespaceName: ns.Name, Scopes: parsed, ClientIP: "192.0.2.1",
	}
}

func as(p *authz.Principal) context.Context {
	return authz.WithPrincipal(context.Background(), p)
}

func requireReason(t *testing.T, err error, reason apperr.Reason) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, reason, apperr.ReasonOf(err), "error: %v", err)
}

func ptr[T any](v T) *T { return &v }

func (e *env) createSecret(t *testing.T, path, value string) *spinneretv1.SecretInfo {
	t.Helper()
	resp, err := e.h.CreateSecret(as(user(e.tenant, authz.RoleAdmin)), connect.NewRequest(&spinneretv1.CreateSecretRequest{
		Namespace: "prod", Path: path, Value: value,
	}))
	require.NoError(t, err)
	return resp.Msg.GetSecret()
}
