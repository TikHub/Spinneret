package authz

import (
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
)

// Fixture identifiers. Tenant A has namespaces prod (sites shop, market) and
// staging (site blog); tenant B has namespace other.
const (
	tenantA     = "ten_a"
	tenantB     = "ten_b"
	nsProd      = "ns_prod"
	nsStaging   = "ns_staging"
	nsOther     = "ns_other"
	siteShop    = "sit_shop"
	siteMarket  = "sit_market"
	siteBlog    = "sit_blog"
	siteOtherB  = "sit_other"
	nsProdName  = "prod"
	nsStageName = "staging"
)

func tenantRes(tenant string) Resource { return Resource{TenantID: tenant} }

func nsRes(tenant, ns, name string) Resource {
	return Resource{TenantID: tenant, NamespaceID: ns, NamespaceName: name}
}

func siteRes(tenant, ns, nsName, siteID, siteName string) Resource {
	return Resource{TenantID: tenant, NamespaceID: ns, NamespaceName: nsName, SiteID: siteID, SiteName: siteName}
}

func user(bindings ...Binding) *Principal {
	return &Principal{Kind: KindUser, ID: "usr_1", Name: "alice", TenantID: tenantA, Bindings: bindings}
}

var (
	resTenantA   = tenantRes(tenantA)
	resProd      = nsRes(tenantA, nsProd, nsProdName)
	resStaging   = nsRes(tenantA, nsStaging, nsStageName)
	resOtherNS   = nsRes(tenantB, nsOther, "other")
	resShop      = siteRes(tenantA, nsProd, nsProdName, siteShop, "shop")
	resMarket    = siteRes(tenantA, nsProd, nsProdName, siteMarket, "market")
	resBlog      = siteRes(tenantA, nsStaging, nsStageName, siteBlog, "blog")
	resOtherSite = siteRes(tenantB, nsOther, "other", siteOtherB, "shop")
)

// TestUserRoleMatrix checks every role against every permission for every
// binding shape and resource level.
func TestUserRoleMatrix(t *testing.T) {
	t.Parallel()
	type check struct {
		res  Resource
		want func(role Role, perm Permission) bool
	}
	has := func(role Role, perm Permission) bool { return RoleHas(role, perm) }
	never := func(Role, Permission) bool { return false }
	siteExempt := func(role Role, perm Permission) bool {
		return RoleHas(role, perm) && (perm == PermProxyRead || perm == PermNamespaceRead)
	}
	shapes := []struct {
		name    string
		binding func(role Role) Binding
		checks  map[string]check
	}{
		{
			name:    "tenant-wide",
			binding: func(role Role) Binding { return Binding{TenantID: tenantA, Role: role} },
			checks: map[string]check{
				"tenant":           {res: resTenantA, want: has},
				"namespace prod":   {res: resProd, want: has},
				"namespace stage":  {res: resStaging, want: has},
				"site shop":        {res: resShop, want: has},
				"site blog":        {res: resBlog, want: has},
				"other tenant ns":  {res: resOtherNS, want: never},
				"other tenant sit": {res: resOtherSite, want: never},
			},
		},
		{
			name:    "namespace prod",
			binding: func(role Role) Binding { return Binding{TenantID: tenantA, Role: role, NamespaceID: nsProd} },
			checks: map[string]check{
				"tenant":          {res: resTenantA, want: never},
				"namespace prod":  {res: resProd, want: has},
				"namespace stage": {res: resStaging, want: never},
				"site shop":       {res: resShop, want: has},
				"site market":     {res: resMarket, want: has},
				"site blog":       {res: resBlog, want: never},
				"other tenant":    {res: resOtherSite, want: never},
			},
		},
		{
			name: "namespace prod site shop",
			binding: func(role Role) Binding {
				return Binding{TenantID: tenantA, Role: role, NamespaceID: nsProd, SiteIDs: []string{siteShop}}
			},
			checks: map[string]check{
				"tenant":          {res: resTenantA, want: never},
				"namespace prod":  {res: resProd, want: siteExempt},
				"namespace stage": {res: resStaging, want: never},
				"site shop":       {res: resShop, want: has},
				"site market":     {res: resMarket, want: never},
				"site blog":       {res: resBlog, want: never},
				"config in prod":  {res: Resource{TenantID: tenantA, NamespaceID: nsProd, ConfigGroup: "app"}, want: siteExempt},
				"secret in prod":  {res: Resource{TenantID: tenantA, NamespaceID: nsProd, SecretPath: "db/pw"}, want: siteExempt},
			},
		},
		{
			name: "tenant-wide site restricted",
			binding: func(role Role) Binding {
				return Binding{TenantID: tenantA, Role: role, SiteIDs: []string{siteShop, siteBlog}}
			},
			checks: map[string]check{
				"tenant":          {res: resTenantA, want: never},
				"namespace prod":  {res: resProd, want: never},
				"namespace stage": {res: resStaging, want: never},
				"site shop":       {res: resShop, want: has},
				"site blog":       {res: resBlog, want: has},
				"site market":     {res: resMarket, want: never},
			},
		},
		{
			name:    "other tenant",
			binding: func(role Role) Binding { return Binding{TenantID: tenantB, Role: role} },
			checks: map[string]check{
				"tenant a":       {res: resTenantA, want: never},
				"namespace prod": {res: resProd, want: never},
				"site shop":      {res: resShop, want: never},
				"tenant b site":  {res: resOtherSite, want: has},
			},
		},
	}
	for _, shape := range shapes {
		for _, role := range Roles() {
			p := user(shape.binding(role))
			for checkName, c := range shape.checks {
				for _, perm := range AllPermissions() {
					name := fmt.Sprintf("%s/%s/%s/%s", shape.name, role, checkName, perm)
					want := c.want(role, perm)
					require.Equal(t, want, p.Can(perm, c.res), name)
					err := p.Require(perm, c.res)
					if want {
						require.NoError(t, err, name)
						continue
					}
					require.Error(t, err, name)
					require.Equal(t, apperr.ReasonPermissionDenied, apperr.ReasonOf(err), name)
				}
			}
		}
	}
}

func TestUserExtraPermissions(t *testing.T) {
	t.Parallel()
	extras := []Permission{
		PermConfigPublish, PermSecretReveal, PermIdentityReveal, PermPolicyPublish,
		PermTenantManage, PermKEKManage, PermTokenWrite, PermUserWrite, PermNamespaceWrite,
		PermLeaseAcquire, PermSecretRead, "bogus:perm",
	}
	viewer := user(Binding{TenantID: tenantA, Role: RoleViewer, NamespaceID: nsProd, Extra: extras})
	for _, perm := range AllPermissions() {
		want := RoleHas(RoleViewer, perm) || ValidExtraPermission(perm)
		require.Equal(t, want, viewer.Can(perm, resShop), perm)
		require.Equal(t, want, viewer.Can(perm, resProd), perm)
		require.False(t, viewer.Can(perm, resStaging) && !RoleHas(RoleViewer, perm), "extras stay namespace-bound: %s", perm)
	}
	require.False(t, viewer.Can("bogus:perm", resProd))

	siteScoped := user(Binding{TenantID: tenantA, Role: RoleOperator, NamespaceID: nsProd,
		SiteIDs: []string{siteShop}, Extra: []Permission{PermConfigPublish, PermIdentityReveal}})
	require.True(t, siteScoped.Can(PermIdentityReveal, resShop))
	require.False(t, siteScoped.Can(PermIdentityReveal, resMarket))
	require.False(t, siteScoped.Can(PermConfigPublish, Resource{TenantID: tenantA, NamespaceID: nsProd, ConfigGroup: "g"}),
		"namespace-level extras require an unrestricted site list")

	invalidRole := user(Binding{TenantID: tenantA, Role: "superuser", Extra: []Permission{PermConfigPublish}})
	for _, perm := range AllPermissions() {
		require.False(t, invalidRole.Can(perm, resShop), "binding with unknown role grants nothing: %s", perm)
	}
}

func TestUserMultipleBindingsUnion(t *testing.T) {
	t.Parallel()
	p := user(
		Binding{TenantID: tenantA, Role: RoleViewer},
		Binding{TenantID: tenantA, Role: RoleAdmin, NamespaceID: nsStaging},
		Binding{TenantID: tenantA, Role: RoleOperator, NamespaceID: nsProd, SiteIDs: []string{siteShop}},
		Binding{TenantID: tenantB, Role: RoleOwner},
	)
	tests := []struct {
		perm Permission
		res  Resource
		want bool
	}{
		{perm: PermSecretReveal, res: resStaging, want: true},
		{perm: PermSecretReveal, res: resProd, want: false},
		{perm: PermIdentityWrite, res: resShop, want: true},
		{perm: PermIdentityWrite, res: resMarket, want: false},
		{perm: PermIdentityRead, res: resMarket, want: true},
		{perm: PermConfigWrite, res: resProd, want: false},
		{perm: PermConfigWrite, res: resStaging, want: true},
		{perm: PermUserWrite, res: resTenantA, want: false},
		{perm: PermUserWrite, res: tenantRes(tenantB), want: true},
		{perm: PermNamespaceWrite, res: resOtherNS, want: true},
		{perm: PermTenantManage, res: tenantRes(tenantB), want: false},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, p.Can(tt.perm, tt.res), "%s on %+v", tt.perm, tt.res)
	}
}

func TestUserDeniedCases(t *testing.T) {
	t.Parallel()
	owner := user(Binding{TenantID: tenantA, Role: RoleOwner})
	tests := []struct {
		name string
		p    *Principal
		perm Permission
		res  Resource
	}{
		{name: "tenant manage for owner", p: owner, perm: PermTenantManage, res: resTenantA},
		{name: "kek manage for owner", p: owner, perm: PermKEKManage, res: resProd},
		{name: "node only lease acquire", p: owner, perm: PermLeaseAcquire, res: resShop},
		{name: "node only report write", p: owner, perm: PermReportWrite, res: resShop},
		{name: "node only secret read", p: owner, perm: PermSecretRead, res: Resource{TenantID: tenantA, NamespaceID: nsProd, SecretPath: "a"}},
		{name: "unknown permission", p: owner, perm: "site:delete", res: resShop},
		{name: "empty permission", p: owner, perm: "", res: resShop},
		{name: "resource without tenant", p: owner, perm: PermSiteRead, res: Resource{NamespaceID: nsProd, SiteID: siteShop}},
		{name: "empty resource", p: owner, perm: PermSiteRead, res: Resource{}},
		{name: "site without namespace", p: owner, perm: PermSiteRead, res: Resource{TenantID: tenantA, SiteID: siteShop}},
		{name: "binding without tenant", p: user(Binding{Role: RoleOwner}), perm: PermSiteRead, res: Resource{NamespaceID: nsProd}},
		{name: "binding without tenant vs tenant resource", p: user(Binding{Role: RoleOwner}), perm: PermSiteRead, res: resProd},
		{name: "no bindings", p: user(), perm: PermSiteRead, res: resProd},
		{name: "site list with empty id", p: user(Binding{TenantID: tenantA, Role: RoleOwner, NamespaceID: nsProd, SiteIDs: []string{""}}), perm: PermConfigRead, res: resProd},
		{name: "unknown principal kind", p: &Principal{Kind: "robot", Bindings: []Binding{{TenantID: tenantA, Role: RoleOwner}}}, perm: PermSiteRead, res: resProd},
		{name: "zero kind with bindings", p: &Principal{Bindings: []Binding{{TenantID: tenantA, Role: RoleOwner}}}, perm: PermSiteRead, res: resProd},
		{name: "zero kind platform admin", p: &Principal{IsPlatformAdmin: true}, perm: PermSiteRead, res: resProd},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.False(t, tt.p.Can(tt.perm, tt.res))
			require.Error(t, tt.p.Require(tt.perm, tt.res))
		})
	}
}

func TestPlatformAdmin(t *testing.T) {
	t.Parallel()
	admin := &Principal{Kind: KindUser, ID: "usr_root", IsPlatformAdmin: true}
	resources := []Resource{{}, resTenantA, resProd, resShop, resOtherSite, {SiteID: "x"}}
	for _, perm := range AllPermissions() {
		for _, res := range resources {
			require.True(t, admin.Can(perm, res), "%s %+v", perm, res)
			require.NoError(t, admin.Require(perm, res))
		}
	}
	require.False(t, admin.Can("bogus", resProd), "unknown permissions are denied even for platform admins")

	tokenClaimingAdmin := &Principal{Kind: KindToken, ID: "tok_1", TenantID: tenantA, NamespaceID: nsProd, IsPlatformAdmin: true}
	for _, perm := range AllPermissions() {
		require.False(t, tokenClaimingAdmin.Can(perm, resShop), "platform admin flag is ignored for tokens: %s", perm)
	}
}

func TestRequireErrors(t *testing.T) {
	t.Parallel()
	viewer := user(Binding{TenantID: tenantA, Role: RoleViewer})
	err := viewer.Require(PermSecretReveal, Resource{TenantID: tenantA, NamespaceID: nsProd, SecretPath: "db/very-secret-path"})
	ae, ok := apperr.As(err)
	require.True(t, ok)
	require.Equal(t, connect.CodePermissionDenied, ae.Code)
	require.Equal(t, apperr.ReasonPermissionDenied, ae.Reason)
	require.Equal(t, "permission secret:reveal required", ae.Message)
	require.NotContains(t, ae.Message, "very-secret-path")
	require.NotContains(t, ae.Message, nsProd)

	token := &Principal{Kind: KindToken, ID: "tok_1", TenantID: tenantA, NamespaceID: nsProd, NamespaceName: nsProdName}
	err = token.Require(PermLeaseAcquire, resShop)
	ae, ok = apperr.As(err)
	require.True(t, ok)
	require.Equal(t, connect.CodePermissionDenied, ae.Code)
	require.Equal(t, apperr.ReasonScopeMissing, ae.Reason)
	require.Equal(t, "token scopes do not grant lease:acquire", ae.Message)
	require.NotContains(t, ae.Message, "shop")

	var nilP *Principal
	err = nilP.Require(PermSiteRead, resProd)
	ae, ok = apperr.As(err)
	require.True(t, ok)
	require.Equal(t, connect.CodeUnauthenticated, ae.Code)
	require.Equal(t, apperr.ReasonSessionInvalid, ae.Reason)

	sys := System("")
	require.NoError(t, sys.Require(PermKEKManage, Resource{}))
	unknown := &Principal{Kind: "robot"}
	require.Equal(t, apperr.ReasonPermissionDenied, apperr.ReasonOf(unknown.Require(PermSiteRead, resProd)))
}
