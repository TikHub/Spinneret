package auth

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/auth/authtest"
	"github.com/Evil0ctal/Spinneret/internal/authz"
)

func permStrings(perms []authz.Permission) []string {
	out := make([]string, len(perms))
	for i, p := range perms {
		out[i] = string(p)
	}
	return out
}

func TestMeTenantWideBindings(t *testing.T) {
	e := newEnv(t)
	w := e.world("acme")
	e.world("globex")
	staging := authtest.Namespace(t, e.pool, w.tenant, "staging")

	me, err := e.users.Me(e.ctx(), w.viewerP())
	require.NoError(t, err)
	require.Equal(t, "acme-viewer", me.User.Username)
	require.Len(t, me.Tenants, 1, "only tenants with bindings")
	ta := me.Tenants[0]
	require.Equal(t, w.tenant, ta.Tenant.ID)
	require.Len(t, ta.Bindings, 1)
	require.Len(t, ta.Namespaces, 2)
	require.Equal(t, "prod", ta.Namespaces[0].Namespace.Name)
	require.Equal(t, staging, ta.Namespaces[1].Namespace.ID)
	require.Equal(t, permStrings(authz.RolePermissions(authz.RoleViewer)), ta.Namespaces[0].Permissions)
	require.Empty(t, ta.Namespaces[0].Sites)
}

func TestMeSiteRestrictedBindings(t *testing.T) {
	e := newEnv(t)
	w := e.world("acme")
	staging := authtest.Namespace(t, e.pool, w.tenant, "staging")
	user := authtest.User(t, e.pool, "scoped", e.hash(testPassword), false)
	viewerProd := authtest.Binding(t, e.pool, user, w.tenant, authz.RoleViewer, w.ns, nil)
	operatorSiteA := authtest.Binding(t, e.pool, user, w.tenant, authz.RoleOperator, w.ns, []string{w.siteA}, authz.PermIdentityReveal)
	p := authtest.UserPrincipal(user, w.tenant, viewerProd, operatorSiteA)

	me, err := e.users.Me(e.ctx(), p)
	require.NoError(t, err)
	require.Len(t, me.Tenants, 1)
	ta := me.Tenants[0]
	require.Len(t, ta.Bindings, 2)
	require.Len(t, ta.Namespaces, 1, "staging is not readable")
	require.NotEqual(t, staging, ta.Namespaces[0].Namespace.ID)
	na := ta.Namespaces[0]
	require.Equal(t, permStrings(authz.RolePermissions(authz.RoleViewer)), na.Permissions)
	require.Len(t, na.Sites, 1, "site B adds nothing beyond the namespace-wide set")
	site := na.Sites[0]
	require.Equal(t, w.siteA, site.SiteID)
	require.Equal(t, "shop", site.SiteName)
	require.Contains(t, site.Permissions, string(authz.PermIdentityWrite))
	require.Contains(t, site.Permissions, string(authz.PermIdentityReveal))
	require.NotContains(t, site.Permissions, string(authz.PermSiteWrite))

	// Only a site-restricted binding: the namespace is readable with the
	// namespace-level read permissions site-scoped users get.
	onlySite := authtest.User(t, e.pool, "only-site", e.hash(testPassword), false)
	b := authtest.Binding(t, e.pool, onlySite, w.tenant, authz.RoleViewer, w.ns, []string{w.siteB})
	me, err = e.users.Me(e.ctx(), authtest.UserPrincipal(onlySite, "", b))
	require.NoError(t, err)
	na = me.Tenants[0].Namespaces[0]
	require.ElementsMatch(t, []string{string(authz.PermNamespaceRead), string(authz.PermProxyRead)}, na.Permissions)
	require.Len(t, na.Sites, 1)
	require.Equal(t, "market", na.Sites[0].SiteName)
	require.Contains(t, na.Sites[0].Permissions, string(authz.PermSiteRead))
}

func TestMePlatformAdminAndErrors(t *testing.T) {
	e := newEnv(t)
	w := e.world("acme")
	e.world("globex")

	me, err := e.users.MeForUser(e.ctx(), w.platformAdmin)
	require.NoError(t, err)
	require.True(t, me.User.IsPlatformAdmin)
	require.Len(t, me.Tenants, 2)
	for _, ta := range me.Tenants {
		require.Empty(t, ta.Bindings)
		require.Len(t, ta.Namespaces, 1)
		require.Len(t, ta.Namespaces[0].Permissions, len(authz.AllPermissions()))
		require.Empty(t, ta.Namespaces[0].Sites)
	}

	_, err = e.users.Me(e.ctx(), authtest.TokenPrincipal("tok_1", w.tenant, w.ns, "prod", "admin"))
	requireReason(t, err, apperr.ReasonPermissionDenied)
	_, err = e.users.Me(e.ctx(), nil)
	requireReason(t, err, apperr.ReasonSessionInvalid)
	_, err = e.users.Me(e.ctx(), authtest.UserPrincipal("usr_missing", ""))
	requireReason(t, err, apperr.ReasonSessionInvalid)
	_, err = e.users.MeForUser(e.ctx(), "usr_missing")
	requireReason(t, err, apperr.ReasonSessionInvalid)
}
