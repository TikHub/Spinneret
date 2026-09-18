package tenancy

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/auth/authtest"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog/catalogtest"
	"github.com/TikHub/Spinneret/internal/tenancy/tenancydb"
	"github.com/TikHub/Spinneret/internal/testutil"
)

type fixture struct {
	svc       *Service
	cat       *catalogtest.Catalog
	installer *authtest.Installer
	rec       *authtest.Recorder
	tenant    string
	ns        string
	owner     *authz.Principal
	admin     *authz.Principal
	viewer    *authz.Principal
}

func newFixture(t *testing.T) (*fixture, func() context.Context) {
	t.Helper()
	pool := testutil.Postgres(t)
	f := &fixture{cat: catalogtest.New(), installer: &authtest.Installer{}, rec: &authtest.Recorder{}}
	f.svc = NewService(pool, f.cat, f.installer, f.rec, slog.New(slog.DiscardHandler))
	f.tenant = authtest.Tenant(t, pool, "acme")
	f.ns = authtest.Namespace(t, pool, f.tenant, "prod")
	ownerID := authtest.User(t, pool, "owner", "x", false)
	f.owner = authtest.UserPrincipal(ownerID, f.tenant, authtest.Binding(t, pool, ownerID, f.tenant, authz.RoleOwner, "", nil))
	viewerID := authtest.User(t, pool, "viewer", "x", false)
	f.viewer = authtest.UserPrincipal(viewerID, f.tenant, authtest.Binding(t, pool, viewerID, f.tenant, authz.RoleViewer, "", nil))
	adminID := authtest.User(t, pool, "root", "x", true)
	f.admin = authtest.AdminPrincipal(adminID, "")
	ctx := func() context.Context {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		t.Cleanup(cancel)
		return c
	}
	return f, ctx
}

func requireReason(t *testing.T, err error, reason apperr.Reason) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, reason, apperr.ReasonOf(err), "error: %v", err)
}

func TestTenantsCRUD(t *testing.T) {
	f, ctx := newFixture(t)

	created, err := f.svc.CreateTenant(ctx(), f.admin, CreateTenantInput{Name: "globex", DisplayName: "Globex", Description: "second", OwnerUserID: f.owner.ID})
	require.NoError(t, err)
	require.Equal(t, "globex", created.Name)
	entry, ok := f.rec.Last(ActionTenantCreate)
	require.True(t, ok)
	require.Equal(t, created.ID, entry.TenantID)

	tests := []struct {
		name   string
		p      *authz.Principal
		in     CreateTenantInput
		reason apperr.Reason
	}{
		{"owner denied", f.owner, CreateTenantInput{Name: "initech"}, apperr.ReasonPermissionDenied},
		{"nil principal", nil, CreateTenantInput{Name: "initech"}, apperr.ReasonSessionInvalid},
		{"duplicate", f.admin, CreateTenantInput{Name: "globex"}, apperr.ReasonAlreadyExists},
		{"invalid name", f.admin, CreateTenantInput{Name: "Bad Name"}, apperr.ReasonInvalidArgument},
		{"invalid display name", f.admin, CreateTenantInput{Name: "initech", DisplayName: "a\x00"}, apperr.ReasonInvalidArgument},
		{"unknown owner", f.admin, CreateTenantInput{Name: "initech", OwnerUserID: "usr_missing"}, apperr.ReasonNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.svc.CreateTenant(ctx(), tc.p, tc.in)
			requireReason(t, err, tc.reason)
		})
	}

	t.Run("list", func(t *testing.T) {
		all, next, total, err := f.svc.ListTenants(ctx(), f.admin, 1, "")
		require.NoError(t, err)
		require.EqualValues(t, 2, total)
		require.Len(t, all, 1)
		require.Equal(t, "acme", all[0].Name)
		rest, next2, _, err := f.svc.ListTenants(ctx(), f.admin, 1, next)
		require.NoError(t, err)
		require.Equal(t, "globex", rest[0].Name)
		require.Empty(t, next2)

		mine, _, total, err := f.svc.ListTenants(ctx(), f.viewer, 0, "")
		require.NoError(t, err)
		require.EqualValues(t, 1, total)
		require.Equal(t, f.tenant, mine[0].ID)

		tok := authtest.TokenPrincipal("tok_1", f.tenant, f.ns, "prod", "lease:acquire")
		mine, _, _, err = f.svc.ListTenants(ctx(), tok, 0, "")
		require.NoError(t, err)
		require.Len(t, mine, 1)

		_, _, _, err = f.svc.ListTenants(ctx(), nil, 0, "")
		requireReason(t, err, apperr.ReasonSessionInvalid)
		_, _, _, err = f.svc.ListTenants(ctx(), f.admin, 0, "*")
		requireReason(t, err, apperr.ReasonInvalidArgument)
	})

	t.Run("update", func(t *testing.T) {
		dn, desc := "Globex Corp", "updated"
		updated, err := f.svc.UpdateTenant(ctx(), f.admin, created.ID, &dn, &desc)
		require.NoError(t, err)
		require.Equal(t, dn, updated.DisplayName)
		require.Equal(t, desc, updated.Description)
		_, err = f.svc.UpdateTenant(ctx(), f.owner, created.ID, &dn, nil)
		requireReason(t, err, apperr.ReasonPermissionDenied)
		_, err = f.svc.UpdateTenant(ctx(), f.admin, "ten_missing", &dn, nil)
		requireReason(t, err, apperr.ReasonNotFound)
		long := string(make([]byte, 2000))
		_, err = f.svc.UpdateTenant(ctx(), f.admin, created.ID, nil, &long)
		requireReason(t, err, apperr.ReasonInvalidArgument)
		_, err = f.svc.UpdateTenant(ctx(), nil, created.ID, nil, nil)
		requireReason(t, err, apperr.ReasonSessionInvalid)
	})

	t.Run("delete", func(t *testing.T) {
		requireReason(t, f.svc.DeleteTenant(ctx(), f.owner, created.ID), apperr.ReasonPermissionDenied)
		requireReason(t, f.svc.DeleteTenant(ctx(), f.admin, f.tenant), apperr.ReasonFailedPrecondition)
		requireReason(t, f.svc.DeleteTenant(ctx(), f.admin, "ten_missing"), apperr.ReasonNotFound)
		requireReason(t, f.svc.DeleteTenant(ctx(), nil, created.ID), apperr.ReasonSessionInvalid)
		require.NoError(t, f.svc.DeleteTenant(ctx(), f.admin, created.ID))
		_, ok := f.rec.Last(ActionTenantDelete)
		require.True(t, ok)
	})
}

func TestNamespacesCRUD(t *testing.T) {
	f, ctx := newFixture(t)

	created, err := f.svc.CreateNamespace(ctx(), f.owner, CreateNamespaceInput{Name: "staging", DisplayName: "Staging", Description: "pre-prod\nenvironment"})
	require.NoError(t, err)
	require.Equal(t, f.tenant, created.TenantID)
	require.Equal(t, []string{created.ID + "|user:" + f.owner.ID}, f.installer.Calls())
	require.Contains(t, f.cat.Calls(), "invalidate:"+created.ID)

	nsAdmin := authtest.UserPrincipal("usr_nsadmin", f.tenant, authz.Binding{TenantID: f.tenant, Role: authz.RoleOwner, NamespaceID: f.ns})
	tests := []struct {
		name   string
		p      *authz.Principal
		in     CreateNamespaceInput
		reason apperr.Reason
	}{
		{"viewer denied", f.viewer, CreateNamespaceInput{Name: "qa"}, apperr.ReasonPermissionDenied},
		{"namespace-scoped owner denied", nsAdmin, CreateNamespaceInput{Name: "qa"}, apperr.ReasonPermissionDenied},
		{"token denied", authtest.TokenPrincipal("tok_1", f.tenant, f.ns, "prod", "admin"), CreateNamespaceInput{Name: "qa"}, apperr.ReasonScopeMissing},
		{"no active tenant", authtest.AdminPrincipal("usr_root", ""), CreateNamespaceInput{Name: "qa"}, apperr.ReasonInvalidArgument},
		{"duplicate", f.owner, CreateNamespaceInput{Name: "staging"}, apperr.ReasonAlreadyExists},
		{"invalid name", f.owner, CreateNamespaceInput{Name: "QA"}, apperr.ReasonInvalidArgument},
		{"unknown tenant", authtest.AdminPrincipal("usr_root", "ten_missing"), CreateNamespaceInput{Name: "qa"}, apperr.ReasonNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.svc.CreateNamespace(ctx(), tc.p, tc.in)
			requireReason(t, err, tc.reason)
		})
	}

	t.Run("installer failure rolls back", func(t *testing.T) {
		f.installer.Err = errors.New("boom")
		defer func() { f.installer.Err = nil }()
		_, err := f.svc.CreateNamespace(ctx(), f.owner, CreateNamespaceInput{Name: "broken"})
		requireReason(t, err, apperr.ReasonInternal)
		list, _, _, err := f.svc.ListNamespaces(ctx(), f.owner, 0, "")
		require.NoError(t, err)
		for _, ns := range list {
			require.NotEqual(t, "broken", ns.Name)
		}
		svc := NewService(f.svc.pool, f.cat, nil, nil, nil)
		_, err = svc.CreateNamespace(ctx(), f.owner, CreateNamespaceInput{Name: "noinstaller"})
		requireReason(t, err, apperr.ReasonInternal)
	})

	t.Run("list filters readable namespaces", func(t *testing.T) {
		all, next, total, err := f.svc.ListNamespaces(ctx(), f.owner, 1, "")
		require.NoError(t, err)
		require.EqualValues(t, 2, total)
		require.Equal(t, "prod", all[0].Name)
		rest, next2, _, err := f.svc.ListNamespaces(ctx(), f.owner, 1, next)
		require.NoError(t, err)
		require.Equal(t, "staging", rest[0].Name)
		require.Empty(t, next2)

		scoped, _, total, err := f.svc.ListNamespaces(ctx(), nsAdmin, 0, "")
		require.NoError(t, err)
		require.EqualValues(t, 1, total)
		require.Equal(t, f.ns, scoped[0].ID)

		// Pages follow byte order regardless of the database collation (which
		// may ignore '-'), so paging visits every namespace exactly once.
		for _, name := range []string{"a-c", "ab", "a0"} {
			authtest.Namespace(t, f.svc.pool, f.tenant, name)
		}
		var names []string
		token := ""
		for {
			page, next, total, err := f.svc.ListNamespaces(ctx(), f.owner, 1, token)
			require.NoError(t, err)
			require.EqualValues(t, 5, total)
			for _, ns := range page {
				names = append(names, ns.Name)
			}
			if next == "" {
				break
			}
			token = next
		}
		require.Equal(t, []string{"a-c", "a0", "ab", "prod", "staging"}, names)
		_, err = f.svc.pool.Exec(ctx(), `DELETE FROM namespaces WHERE tenant_id = $1 AND name IN ('a-c', 'ab', 'a0')`, f.tenant)
		require.NoError(t, err)

		_, _, _, err = f.svc.ListNamespaces(ctx(), authtest.UserPrincipal("u", ""), 0, "")
		requireReason(t, err, apperr.ReasonInvalidArgument)
		_, _, _, err = f.svc.ListNamespaces(ctx(), f.owner, 0, "@@")
		requireReason(t, err, apperr.ReasonInvalidArgument)
	})

	t.Run("update", func(t *testing.T) {
		dn := "Staging 2"
		updated, err := f.svc.UpdateNamespace(ctx(), f.owner, created.ID, &dn, nil)
		require.NoError(t, err)
		require.Equal(t, dn, updated.DisplayName)
		require.Equal(t, "pre-prod\nenvironment", updated.Description)
		_, err = f.svc.UpdateNamespace(ctx(), f.viewer, created.ID, &dn, nil)
		requireReason(t, err, apperr.ReasonPermissionDenied)
		_, err = f.svc.UpdateNamespace(ctx(), nsAdmin, created.ID, &dn, nil)
		requireReason(t, err, apperr.ReasonPermissionDenied)
		_, err = f.svc.UpdateNamespace(ctx(), f.owner, "ns_missing", &dn, nil)
		requireReason(t, err, apperr.ReasonNotFound)
		bad := "x\x07"
		_, err = f.svc.UpdateNamespace(ctx(), f.owner, created.ID, &bad, nil)
		requireReason(t, err, apperr.ReasonInvalidArgument)

		otherTenant := authtest.UserPrincipal("usr_o", "ten_other", authz.Binding{TenantID: "ten_other", Role: authz.RoleOwner})
		_, err = f.svc.UpdateNamespace(ctx(), otherTenant, created.ID, &dn, nil)
		requireReason(t, err, apperr.ReasonNotFound)
		_, err = f.svc.UpdateNamespace(ctx(), f.admin, created.ID, &dn, nil)
		require.NoError(t, err, "platform admins without an active tenant address any namespace")
		_, err = f.svc.UpdateNamespace(ctx(), nil, created.ID, &dn, nil)
		requireReason(t, err, apperr.ReasonSessionInvalid)
	})

	t.Run("delete", func(t *testing.T) {
		pool := f.svc.pool
		siteID := authtest.Site(t, pool, created.ID, "shop")
		_, err := pool.Exec(ctx(), `INSERT INTO api_tokens (id, tenant_id, namespace_id, name, token_prefix, token_hash)
			VALUES ('tok_x', $1, $2, 'n', 'spn_', '\x01')`, f.tenant, created.ID)
		require.NoError(t, err)
		// Regression: revoked and expired tokens cannot be deleted through the API and must not block the
		// deletion of their namespace forever; they are deleted with it.
		_, err = pool.Exec(ctx(), `INSERT INTO api_tokens (id, tenant_id, namespace_id, name, token_prefix, token_hash, revoked_at, expires_at)
			VALUES ('tok_revoked', $1, $2, 'revoked', 'spn_', '\x02', now(), NULL),
			       ('tok_expired', $1, $2, 'expired', 'spn_', '\x03', NULL, now() - interval '1 minute')`, f.tenant, created.ID)
		require.NoError(t, err)

		err = f.svc.DeleteNamespace(ctx(), f.owner, created.ID)
		requireReason(t, err, apperr.ReasonFailedPrecondition)
		require.Contains(t, err.Error(), "1 sites, 1 API tokens")

		_, err = pool.Exec(ctx(), `DELETE FROM sites WHERE id = $1`, siteID)
		require.NoError(t, err)
		err = f.svc.DeleteNamespace(ctx(), f.owner, created.ID)
		requireReason(t, err, apperr.ReasonFailedPrecondition)
		require.Contains(t, err.Error(), "namespace \"")
		require.Contains(t, err.Error(), ": 1 API tokens")
		_, err = pool.Exec(ctx(), `UPDATE api_tokens SET revoked_at = now() WHERE id = 'tok_x'`)
		require.NoError(t, err)

		requireReason(t, f.svc.DeleteNamespace(ctx(), f.viewer, created.ID), apperr.ReasonPermissionDenied)
		requireReason(t, f.svc.DeleteNamespace(ctx(), f.owner, "ns_missing"), apperr.ReasonNotFound)
		require.NoError(t, f.svc.DeleteNamespace(ctx(), f.owner, created.ID))
		var tokens int
		require.NoError(t, pool.QueryRow(ctx(), `SELECT count(*) FROM api_tokens WHERE namespace_id = $1`, created.ID).Scan(&tokens))
		require.Zero(t, tokens, "revoked and expired tokens are deleted with the namespace")
		require.Contains(t, f.cat.Calls(), "invalidate:"+created.ID)
		_, ok := f.rec.Last(ActionNamespaceDelete)
		require.True(t, ok)
		requireReason(t, f.svc.DeleteNamespace(ctx(), f.owner, created.ID), apperr.ReasonNotFound)
	})
}

func TestUsageSummary(t *testing.T) {
	require.Empty(t, usageSummary(tenancydb.TenancyNamespaceUsageRow{}))
	require.Equal(t, "2 proxies, 1 config items, 3 secrets",
		usageSummary(tenancydb.TenancyNamespaceUsageRow{Proxies: 2, ConfigItems: 1, Secrets: 3}))
}

func TestBootstrapDelegates(t *testing.T) {
	pool := testutil.Postgres(t)
	installer := &authtest.Installer{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	id, err := BootstrapPlatformAdmin(ctx, pool, "root", "long-enough-pw", "acme", "prod", installer)
	require.NoError(t, err)
	require.NotEmpty(t, id)
	require.Len(t, installer.Calls(), 1)
}

func TestInvalidateWithoutCatalog(t *testing.T) {
	svc := NewService(nil, nil, nil, nil, nil)
	svc.invalidate(context.Background(), "ns_1")
	require.NoError(t, internalUnlessApp(nil))
	require.ErrorIs(t, internalUnlessApp(context.Canceled), context.Canceled)
}
