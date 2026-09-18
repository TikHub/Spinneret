package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/audit"
	"github.com/Evil0ctal/Spinneret/internal/auth/authtest"
	"github.com/Evil0ctal/Spinneret/internal/authz"
)

func TestListAuditLogs(t *testing.T) {
	e := newEnv(t)
	w := e.world("acme")
	other := e.world("globex")
	now := time.Now()
	for i, action := range []string{"identity.ban", "secret.read", "config.publish", "identity.ban"} {
		authtest.AuditRow(t, e.pool, audit.Entry{
			CreatedAt: now.Add(-time.Duration(i) * time.Minute), TenantID: w.tenant, NamespaceID: w.ns,
			ActorKind: "user", ActorID: w.owner, ActorName: "acme-owner", Action: action, ResourceKind: "identity", ResourceID: "idt_1",
		})
	}
	authtest.AuditRow(t, e.pool, audit.Entry{TenantID: w.tenant, Action: "user.create", ActorKind: "user", ActorName: "acme-owner", Result: audit.ResultDenied})
	authtest.AuditRow(t, e.pool, audit.Entry{CreatedAt: now.Add(-48 * time.Hour), TenantID: w.tenant, Action: "old.entry"})
	authtest.AuditRow(t, e.pool, audit.Entry{TenantID: other.tenant, Action: "other.tenant"})
	authtest.AuditRow(t, e.pool, audit.Entry{Action: ActionLogin, ActorKind: "system"})

	t.Run("pagination newest first", func(t *testing.T) {
		var got []AuditRecord
		token := ""
		for {
			page, next, err := e.auditLog.List(e.ctx(), w.viewerP(), AuditQuery{PageSize: 2, PageToken: token})
			require.NoError(t, err)
			got = append(got, page...)
			if next == "" {
				break
			}
			token = next
		}
		require.Len(t, got, 5, "default range excludes the 48h old entry and other tenants")
		for i := 1; i < len(got); i++ {
			require.False(t, got[i].CreatedAt.After(got[i-1].CreatedAt))
		}
		require.Equal(t, "prod", got[1].Namespace)
		require.Equal(t, map[string]any{"k": "v"}, got[1].Details)
	})
	t.Run("filters", func(t *testing.T) {
		ref := w.nsRef()
		tests := []struct {
			name string
			q    AuditQuery
			want int
		}{
			{"namespace", AuditQuery{Namespace: &ref}, 4},
			{"action", AuditQuery{Action: "identity.ban"}, 2},
			{"actor by name", AuditQuery{Actor: "acme-owner"}, 5},
			{"actor by id", AuditQuery{Actor: w.owner}, 4},
			{"resource", AuditQuery{ResourceKind: "identity", ResourceID: "idt_1"}, 4},
			{"result", AuditQuery{Result: audit.ResultDenied}, 1},
			{"explicit range", AuditQuery{Start: ptr(now.Add(-72 * time.Hour)), End: ptr(now.Add(-24 * time.Hour))}, 1},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				got, _, err := e.auditLog.List(e.ctx(), w.ownerP(), tc.q)
				require.NoError(t, err)
				require.Len(t, got, tc.want)
			})
		}
	})
	t.Run("permissions", func(t *testing.T) {
		nsViewer := authtest.UserPrincipal("usr_nsv", w.tenant, authz.Binding{TenantID: w.tenant, Role: authz.RoleViewer, NamespaceID: w.ns})
		// Without a namespace filter, namespace-scoped readers see the entries
		// of their namespaces only (no tenant-level entries).
		got, _, err := e.auditLog.List(e.ctx(), nsViewer, AuditQuery{})
		require.NoError(t, err)
		require.Len(t, got, 4)
		for _, r := range got {
			require.Equal(t, w.ns, r.NamespaceID)
		}
		ref := w.nsRef()
		got, _, err = e.auditLog.List(e.ctx(), nsViewer, AuditQuery{Namespace: &ref})
		require.NoError(t, err)
		require.Len(t, got, 4)

		// Site-restricted bindings do not grant the namespace audit log.
		siteViewer := authtest.UserPrincipal("usr_sv", w.tenant,
			authz.Binding{TenantID: w.tenant, Role: authz.RoleViewer, NamespaceID: w.ns, SiteIDs: []string{w.siteA}})
		_, _, err = e.auditLog.List(e.ctx(), siteViewer, AuditQuery{})
		requireReason(t, err, apperr.ReasonPermissionDenied)
		_, _, err = e.auditLog.List(e.ctx(), siteViewer, AuditQuery{Namespace: &ref})
		requireReason(t, err, apperr.ReasonPermissionDenied)
		_, _, err = e.auditLog.List(e.ctx(), other.viewerP(), AuditQuery{})
		require.NoError(t, err, "tenant-wide readers of another tenant see their own tenant")
		// A binding in another tenant grants nothing in this one.
		foreign := authtest.UserPrincipal("usr_f", w.tenant, authz.Binding{TenantID: other.tenant, Role: authz.RoleOwner})
		_, _, err = e.auditLog.List(e.ctx(), foreign, AuditQuery{})
		requireReason(t, err, apperr.ReasonPermissionDenied)

		_, _, err = e.auditLog.List(e.ctx(), other.ownerP(), AuditQuery{Namespace: &ref})
		requireReason(t, err, apperr.ReasonPermissionDenied)
		_, _, err = e.auditLog.List(e.ctx(), authtest.UserPrincipal(w.owner, ""), AuditQuery{})
		requireReason(t, err, apperr.ReasonInvalidArgument)
		_, _, err = e.auditLog.List(e.ctx(), nil, AuditQuery{})
		requireReason(t, err, apperr.ReasonSessionInvalid)

		tokenP := authtest.TokenPrincipal("tok_1", w.tenant, w.ns, "prod", "admin")
		got, _, err = e.auditLog.List(e.ctx(), tokenP, AuditQuery{Namespace: &ref})
		require.NoError(t, err)
		require.Len(t, got, 4)
		got, _, err = e.auditLog.List(e.ctx(), tokenP, AuditQuery{})
		require.NoError(t, err)
		require.Len(t, got, 4, "tokens read the audit log of their namespace only")
		_, _, err = e.auditLog.List(e.ctx(), authtest.TokenPrincipal("tok_2", w.tenant, w.ns, "prod", "config:read"), AuditQuery{})
		requireReason(t, err, apperr.ReasonScopeMissing)

		platform, _, err := e.auditLog.List(e.ctx(), authtest.AdminPrincipal(w.platformAdmin, ""), AuditQuery{})
		require.NoError(t, err)
		require.Len(t, platform, 1)
		require.Equal(t, ActionLogin, platform[0].Action)
	})
	t.Run("validation", func(t *testing.T) {
		_, _, err := e.auditLog.List(e.ctx(), w.ownerP(), AuditQuery{Start: ptr(now), End: ptr(now)})
		requireReason(t, err, apperr.ReasonInvalidArgument)
		_, _, err = e.auditLog.List(e.ctx(), w.ownerP(), AuditQuery{Action: string(make([]byte, 300))})
		requireReason(t, err, apperr.ReasonInvalidArgument)
		_, _, err = e.auditLog.List(e.ctx(), w.ownerP(), AuditQuery{PageToken: "~"})
		requireReason(t, err, apperr.ReasonInvalidArgument)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, _, err = e.auditLog.List(ctx, w.ownerP(), AuditQuery{})
		require.Error(t, err)
	})
}

func TestBootstrapPlatformAdmin(t *testing.T) {
	e := newEnv(t)
	installer := &authtest.Installer{}

	tests := []struct {
		name                                  string
		username, password, tenant, namespace string
		installer                             NamespaceInstaller
	}{
		{"invalid username", "X", "long-enough-pw", "acme", "prod", installer},
		{"weak password", "root", "short", "acme", "prod", installer},
		{"invalid tenant", "root", "long-enough-pw", "Acme", "prod", installer},
		{"invalid namespace", "root", "long-enough-pw", "acme", "p", installer},
		{"nil installer", "root", "long-enough-pw", "acme", "prod", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := BootstrapPlatformAdmin(e.ctx(), e.pool, tc.username, tc.password, tc.tenant, tc.namespace, tc.installer)
			require.Error(t, err)
		})
	}

	failing := &authtest.Installer{Err: errors.New("policies unavailable")}
	_, err := BootstrapPlatformAdmin(e.ctx(), e.pool, "root", "long-enough-pw", "acme", "prod", failing)
	requireReason(t, err, apperr.ReasonInternal)
	var n int
	require.NoError(t, e.pool.QueryRow(e.ctx(), `SELECT count(*) FROM tenants`).Scan(&n))
	require.Zero(t, n, "the failed bootstrap was rolled back")

	userID, err := BootstrapPlatformAdmin(e.ctx(), e.pool, "Root", "long-enough-pw", "acme", "prod", installer)
	require.NoError(t, err)
	require.Len(t, installer.Calls(), 1)
	require.Contains(t, installer.Calls()[0], "|"+bootstrapActor)

	var admin bool
	var tenantID string
	require.NoError(t, e.pool.QueryRow(e.ctx(), `SELECT u.is_platform_admin, rb.tenant_id FROM users u
		JOIN role_bindings rb ON rb.user_id = u.id AND rb.role = 'owner' AND rb.namespace_id IS NULL WHERE u.id = $1 AND u.username = 'root'`, userID).
		Scan(&admin, &tenantID))
	require.True(t, admin)
	var nsCount int
	require.NoError(t, e.pool.QueryRow(e.ctx(), `SELECT count(*) FROM namespaces WHERE tenant_id = $1 AND name = 'prod'`, tenantID).Scan(&nsCount))
	require.Equal(t, 1, nsCount)

	_, err = BootstrapPlatformAdmin(e.ctx(), e.pool, "root2", "long-enough-pw", "acme", "prod", installer)
	requireReason(t, err, apperr.ReasonFailedPrecondition)

	// Login works with the production hash parameters.
	_, _, err = e.users.Login(e.ctx(), "root", "long-enough-pw", "", "")
	require.NoError(t, err)
}

func TestBootstrapReusesExistingTenantAndNamespace(t *testing.T) {
	e := newEnv(t)
	tenantID := authtest.Tenant(t, e.pool, "acme")
	authtest.Namespace(t, e.pool, tenantID, "prod")
	installer := &authtest.Installer{}
	_, err := BootstrapPlatformAdmin(e.ctx(), e.pool, "root", "long-enough-pw", "acme", "prod", installer)
	require.NoError(t, err)
	require.Empty(t, installer.Calls(), "defaults are only installed for new namespaces")
	var tenants int
	require.NoError(t, e.pool.QueryRow(e.ctx(), `SELECT count(*) FROM tenants`).Scan(&tenants))
	require.Equal(t, 1, tenants)

	var exists bool
	err = e.pool.QueryRow(e.ctx(), `SELECT true FROM users WHERE username = 'nobody'`).Scan(&exists)
	require.ErrorIs(t, err, pgx.ErrNoRows)
}

func ptr[T any](v T) *T { return &v }
