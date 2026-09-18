// Package authtest provides PostgreSQL fixtures and fakes for tests of the
// auth, tenancy and access API packages. It must only be imported by tests.
package authtest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/audit"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/pkg/idgen"
)

// fixtureTimeout guards against a hung fixture; it is not a latency assertion.
// It is generous on purpose: in CI the whole Go suite runs with -race against one
// PostgreSQL service container, and a ten-second bound turned that contention into
// "i/o timeout" on a plain INSERT. The ClickHouse helpers allow three minutes for
// the same reason.
const fixtureTimeout = time.Minute

func exec(t testing.TB, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), fixtureTimeout)
	defer cancel()
	_, err := pool.Exec(ctx, sql, args...)
	require.NoError(t, err)
}

// Tenant inserts a tenant and returns its ID.
func Tenant(t testing.TB, pool *pgxpool.Pool, name string) string {
	t.Helper()
	id := idgen.New(idgen.Tenant)
	exec(t, pool, `INSERT INTO tenants (id, name, display_name) VALUES ($1, $2, $2)`, id, name)
	return id
}

// Namespace inserts a namespace and returns its ID.
func Namespace(t testing.TB, pool *pgxpool.Pool, tenantID, name string) string {
	t.Helper()
	id := idgen.New(idgen.Namespace)
	exec(t, pool, `INSERT INTO namespaces (id, tenant_id, name, display_name) VALUES ($1, $2, $3, $3)`, id, tenantID, name)
	return id
}

// Site inserts a site and returns its ID.
func Site(t testing.TB, pool *pgxpool.Pool, namespaceID, name string) string {
	t.Helper()
	id := idgen.New(idgen.Site)
	exec(t, pool, `INSERT INTO sites (id, namespace_id, name) VALUES ($1, $2, $3)`, id, namespaceID, name)
	return id
}

// User inserts a user with a precomputed password hash and returns its ID.
func User(t testing.TB, pool *pgxpool.Pool, username, passwordHash string, platformAdmin bool) string {
	t.Helper()
	id := idgen.New(idgen.User)
	exec(t, pool, `INSERT INTO users (id, username, display_name, password_hash, is_platform_admin) VALUES ($1, $2, $2, $3, $4)`,
		id, username, passwordHash, platformAdmin)
	return id
}

// DisableUser sets the disabled flag of a user.
func DisableUser(t testing.TB, pool *pgxpool.Pool, userID string) {
	t.Helper()
	exec(t, pool, `UPDATE users SET disabled = true WHERE id = $1`, userID)
}

// Binding inserts a role binding and returns it in authz form.
func Binding(t testing.TB, pool *pgxpool.Pool, userID, tenantID string, role authz.Role, namespaceID string, siteIDs []string, extra ...authz.Permission) authz.Binding {
	t.Helper()
	id := idgen.New(idgen.RoleBinding)
	var ns *string
	if namespaceID != "" {
		ns = &namespaceID
	}
	if siteIDs == nil {
		siteIDs = []string{}
	}
	extras := make([]string, len(extra))
	for i, e := range extra {
		extras[i] = string(e)
	}
	exec(t, pool, `INSERT INTO role_bindings (id, user_id, tenant_id, role, namespace_id, site_ids, extra_permissions)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, id, userID, tenantID, string(role), ns, siteIDs, extras)
	return authz.Binding{ID: id, TenantID: tenantID, Role: role, NamespaceID: namespaceID, SiteIDs: siteIDs, Extra: extra}
}

// AuditRow inserts an audit log row directly (bypassing the async writer).
func AuditRow(t testing.TB, pool *pgxpool.Pool, e audit.Entry) string {
	t.Helper()
	id := idgen.New(idgen.Audit)
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now()
	}
	if e.Result == "" {
		e.Result = audit.ResultOK
	}
	if e.ActorKind == "" {
		e.ActorKind = string(authz.KindUser)
	}
	exec(t, pool, `INSERT INTO audit_logs (id, created_at, tenant_id, namespace_id, actor_kind, actor_id, actor_name, action,
		resource_kind, resource_id, resource_name, result, ip, user_agent, details)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, '{"k":"v"}')`,
		id, e.CreatedAt, e.TenantID, e.NamespaceID, e.ActorKind, e.ActorID, e.ActorName, e.Action, e.ResourceKind,
		e.ResourceID, e.ResourceName, e.Result, e.IP, e.UserAgent)
	return id
}

// UserPrincipal builds a console user principal.
func UserPrincipal(userID, tenantID string, bindings ...authz.Binding) *authz.Principal {
	return &authz.Principal{Kind: authz.KindUser, ID: userID, Name: userID, TenantID: tenantID, Bindings: bindings, ClientIP: "192.0.2.10"}
}

// AdminPrincipal builds a platform administrator principal.
func AdminPrincipal(userID, tenantID string) *authz.Principal {
	return &authz.Principal{Kind: authz.KindUser, ID: userID, Name: userID, TenantID: tenantID, IsPlatformAdmin: true}
}

// TokenPrincipal builds an API token principal.
func TokenPrincipal(tokenID, tenantID, namespaceID, namespaceName string, scopes ...string) *authz.Principal {
	parsed := make([]authz.Scope, 0, len(scopes))
	for _, s := range scopes {
		sc, err := authz.ParseScope(s)
		if err != nil {
			panic(err)
		}
		parsed = append(parsed, sc)
	}
	return &authz.Principal{Kind: authz.KindToken, ID: tokenID, Name: tokenID, TenantID: tenantID, NamespaceID: namespaceID,
		NamespaceName: namespaceName, Scopes: parsed}
}

// Recorder captures audit entries.
type Recorder struct {
	mu      sync.Mutex
	entries []audit.Entry
}

// Record implements audit.Recorder.
func (r *Recorder) Record(_ context.Context, e audit.Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, e)
}

// Entries returns a copy of the captured entries.
func (r *Recorder) Entries() []audit.Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]audit.Entry(nil), r.entries...)
}

// Last returns the most recent entry with the given action.
func (r *Recorder) Last(action string) (audit.Entry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.entries) - 1; i >= 0; i-- {
		if r.entries[i].Action == action {
			return r.entries[i], true
		}
	}
	return audit.Entry{}, false
}

// Installer is a fake namespace installer that records calls and can fail.
type Installer struct {
	mu    sync.Mutex
	calls []string
	// Err is returned by InstallNamespaceDefaults when set.
	Err error
}

// InstallNamespaceDefaults records the namespace and verifies that the
// transaction can see the new namespace row.
func (i *Installer) InstallNamespaceDefaults(ctx context.Context, tx pgx.Tx, namespaceID, actor string) error {
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM namespaces WHERE id = $1`, namespaceID).Scan(&n); err != nil {
		return err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if n == 1 {
		i.calls = append(i.calls, namespaceID+"|"+actor)
	}
	return i.Err
}

// Calls returns the recorded "<namespace id>|<actor>" calls.
func (i *Installer) Calls() []string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]string(nil), i.calls...)
}
