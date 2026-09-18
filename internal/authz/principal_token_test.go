package authz

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
)

func token(t *testing.T, scopes ...string) *Principal {
	t.Helper()
	parsed, err := ParseScopes(scopes)
	require.NoError(t, err)
	return &Principal{
		Kind: KindToken, ID: "tok_1", Name: "crawler", TenantID: tenantA,
		NamespaceID: nsProd, NamespaceName: nsProdName, Scopes: parsed,
	}
}

func cfgRes(group string) Resource {
	r := resProd
	r.ConfigGroup = group
	return r
}

func secRes(path string) Resource {
	r := resProd
	r.SecretPath = path
	return r
}

func TestTokenScopes(t *testing.T) {
	t.Parallel()
	shopNoName := resShop
	shopNoName.SiteName = ""
	shopUpper := resShop
	shopUpper.SiteName = "Shop"
	secNoNSName := secRes("db/pw")
	secNoNSName.NamespaceName = ""
	secWrongNSName := secRes("db/pw")
	secWrongNSName.NamespaceName = nsStageName

	tests := []struct {
		name   string
		scopes []string
		perm   Permission
		res    Resource
		want   bool
	}{
		// lease:acquire / report:write
		{name: "lease any site shop", scopes: []string{"lease:acquire"}, perm: PermLeaseAcquire, res: resShop, want: true},
		{name: "lease any site market", scopes: []string{"lease:acquire"}, perm: PermLeaseAcquire, res: resMarket, want: true},
		{name: "lease does not grant report", scopes: []string{"lease:acquire"}, perm: PermReportWrite, res: resShop, want: false},
		{name: "lease site arg match", scopes: []string{"lease:acquire:shop"}, perm: PermLeaseAcquire, res: resShop, want: true},
		{name: "lease site arg mismatch", scopes: []string{"lease:acquire:shop"}, perm: PermLeaseAcquire, res: resMarket, want: false},
		{name: "lease site arg requires site name", scopes: []string{"lease:acquire:shop"}, perm: PermLeaseAcquire, res: shopNoName, want: false},
		{name: "lease site arg exact case", scopes: []string{"lease:acquire:shop"}, perm: PermLeaseAcquire, res: shopUpper, want: false},
		{name: "lease site arg on namespace resource", scopes: []string{"lease:acquire:shop"}, perm: PermLeaseAcquire, res: resProd, want: false},
		{name: "lease site arg union", scopes: []string{"lease:acquire:shop", "lease:acquire:market"}, perm: PermLeaseAcquire, res: resMarket, want: true},
		{name: "report site arg", scopes: []string{"report:write:shop"}, perm: PermReportWrite, res: resShop, want: true},
		{name: "report site arg other site", scopes: []string{"report:write:shop"}, perm: PermReportWrite, res: resMarket, want: false},
		{name: "report does not grant lease", scopes: []string{"report:write:shop"}, perm: PermLeaseAcquire, res: resShop, want: false},
		{name: "lease does not grant site read", scopes: []string{"lease:acquire", "report:write"}, perm: PermSiteRead, res: resShop, want: false},

		// config:read / config:publish
		{name: "config read all groups", scopes: []string{"config:read"}, perm: PermConfigRead, res: cfgRes("crawler"), want: true},
		{name: "config read without group", scopes: []string{"config:read"}, perm: PermConfigRead, res: resProd, want: true},
		{name: "config read no write", scopes: []string{"config:read"}, perm: PermConfigWrite, res: cfgRes("crawler"), want: false},
		{name: "config read no publish", scopes: []string{"config:read"}, perm: PermConfigPublish, res: cfgRes("crawler"), want: false},
		{name: "config read glob match", scopes: []string{"config:read:crawler*"}, perm: PermConfigRead, res: cfgRes("crawler-hk"), want: true},
		{name: "config read glob exact", scopes: []string{"config:read:crawler*"}, perm: PermConfigRead, res: cfgRes("crawler"), want: true},
		{name: "config read glob mismatch", scopes: []string{"config:read:crawler*"}, perm: PermConfigRead, res: cfgRes("api"), want: false},
		{name: "config read glob empty group", scopes: []string{"config:read:crawler*"}, perm: PermConfigRead, res: resProd, want: false},
		{name: "config read star empty group", scopes: []string{"config:read:*"}, perm: PermConfigRead, res: resProd, want: true},
		{name: "config read literal group", scopes: []string{"config:read:app"}, perm: PermConfigRead, res: cfgRes("app2"), want: false},
		{name: "config publish grants read", scopes: []string{"config:publish:app"}, perm: PermConfigRead, res: cfgRes("app"), want: true},
		{name: "config publish grants write", scopes: []string{"config:publish:app"}, perm: PermConfigWrite, res: cfgRes("app"), want: true},
		{name: "config publish grants publish", scopes: []string{"config:publish:app"}, perm: PermConfigPublish, res: cfgRes("app"), want: true},
		{name: "config publish glob mismatch", scopes: []string{"config:publish:app"}, perm: PermConfigPublish, res: cfgRes("other"), want: false},
		{name: "config publish all", scopes: []string{"config:publish"}, perm: PermConfigPublish, res: cfgRes("_runtime"), want: true},
		{name: "config scopes do not grant secret list", scopes: []string{"config:publish"}, perm: PermSecretList, res: resProd, want: false},

		// secret:read
		{name: "secret namespace glob", scopes: []string{"secret:read:prod/*"}, perm: PermSecretRead, res: secRes("db/password"), want: true},
		{name: "secret namespace glob nested", scopes: []string{"secret:read:prod/*"}, perm: PermSecretRead, res: secRes("a/b/c"), want: true},
		{name: "secret path prefix glob", scopes: []string{"secret:read:prod/db/*"}, perm: PermSecretRead, res: secRes("db/password"), want: true},
		{name: "secret path prefix mismatch", scopes: []string{"secret:read:prod/db/*"}, perm: PermSecretRead, res: secRes("cache/password"), want: false},
		{name: "secret exact path", scopes: []string{"secret:read:prod/db/password"}, perm: PermSecretRead, res: secRes("db/password"), want: true},
		{name: "secret exact path other", scopes: []string{"secret:read:prod/db/password"}, perm: PermSecretRead, res: secRes("db/password2"), want: false},
		{name: "secret question mark", scopes: []string{"secret:read:prod/db/?"}, perm: PermSecretRead, res: secRes("db/a"), want: true},
		{name: "secret question mark too long", scopes: []string{"secret:read:prod/db/?"}, perm: PermSecretRead, res: secRes("db/ab"), want: false},
		{name: "secret other namespace glob", scopes: []string{"secret:read:staging/*"}, perm: PermSecretRead, res: secRes("db/password"), want: false},
		{name: "secret glob without namespace", scopes: []string{"secret:read:db/*"}, perm: PermSecretRead, res: secRes("db/password"), want: false},
		{name: "secret star", scopes: []string{"secret:read:*"}, perm: PermSecretRead, res: secRes("x"), want: true},
		{name: "secret namespace prefix trick", scopes: []string{"secret:read:prod/*"}, perm: PermSecretRead, res: func() Resource { r := secRes("x"); r.NamespaceName = "prod-evil"; return r }(), want: false},
		{name: "secret empty path", scopes: []string{"secret:read:*"}, perm: PermSecretRead, res: resProd, want: false},
		{name: "secret dot dot path", scopes: []string{"secret:read:prod/public/*"}, perm: PermSecretRead, res: secRes("public/../private/key"), want: false},
		{name: "secret leading slash", scopes: []string{"secret:read:prod/*"}, perm: PermSecretRead, res: secRes("/db"), want: false},
		{name: "secret double slash", scopes: []string{"secret:read:prod/*"}, perm: PermSecretRead, res: secRes("db//pw"), want: false},
		{name: "secret namespace name from principal", scopes: []string{"secret:read:prod/*"}, perm: PermSecretRead, res: secNoNSName, want: true},
		{name: "secret namespace name mismatch", scopes: []string{"secret:read:*"}, perm: PermSecretRead, res: secWrongNSName, want: false},
		{name: "secret read no list", scopes: []string{"secret:read:*"}, perm: PermSecretList, res: secRes("x"), want: false},
		{name: "secret read no reveal", scopes: []string{"secret:read:*"}, perm: PermSecretReveal, res: secRes("x"), want: false},
		{name: "secret read no write", scopes: []string{"secret:read:*"}, perm: PermSecretWrite, res: secRes("x"), want: false},

		// identity:write
		{name: "identity write read", scopes: []string{"identity:write"}, perm: PermIdentityRead, res: resShop, want: true},
		{name: "identity write write", scopes: []string{"identity:write"}, perm: PermIdentityWrite, res: resMarket, want: true},
		{name: "identity write operate", scopes: []string{"identity:write"}, perm: PermIdentityOperate, res: resShop, want: true},
		{name: "identity write namespace level", scopes: []string{"identity:write"}, perm: PermIdentityRead, res: resProd, want: true},
		{name: "identity write no reveal", scopes: []string{"identity:write"}, perm: PermIdentityReveal, res: resShop, want: false},
		{name: "identity write no site read", scopes: []string{"identity:write"}, perm: PermSiteRead, res: resShop, want: false},
		{name: "identity write site arg", scopes: []string{"identity:write:shop"}, perm: PermIdentityWrite, res: resShop, want: true},
		{name: "identity write site arg other", scopes: []string{"identity:write:shop"}, perm: PermIdentityWrite, res: resMarket, want: false},
		{name: "identity write site arg namespace level", scopes: []string{"identity:write:shop"}, perm: PermIdentityRead, res: resProd, want: false},

		// proxy:write
		{name: "proxy write read", scopes: []string{"proxy:write"}, perm: PermProxyRead, res: resProd, want: true},
		{name: "proxy write write", scopes: []string{"proxy:write"}, perm: PermProxyWrite, res: resProd, want: true},
		{name: "proxy write operate", scopes: []string{"proxy:write"}, perm: PermProxyOperate, res: resProd, want: true},
		{name: "proxy write no identity", scopes: []string{"proxy:write"}, perm: PermIdentityRead, res: resShop, want: false},

		// namespace, tenant and principal binding
		{name: "other namespace same tenant", scopes: []string{"admin", "lease:acquire"}, perm: PermLeaseAcquire, res: resBlog, want: false},
		{name: "other namespace admin", scopes: []string{"admin"}, perm: PermSiteRead, res: resStaging, want: false},
		{name: "other tenant", scopes: []string{"admin", "lease:acquire"}, perm: PermSiteRead, res: resOtherSite, want: false},
		{name: "other tenant same namespace id", scopes: []string{"admin"}, perm: PermSiteRead, res: Resource{TenantID: tenantB, NamespaceID: nsProd}, want: false},
		{name: "tenant level resource", scopes: []string{"admin"}, perm: PermNotifyRead, res: resTenantA, want: false},
		{name: "resource without tenant", scopes: []string{"admin"}, perm: PermSiteRead, res: Resource{NamespaceID: nsProd}, want: false},
		{name: "site without namespace", scopes: []string{"lease:acquire"}, perm: PermLeaseAcquire, res: Resource{TenantID: tenantA, SiteID: siteShop, SiteName: "shop"}, want: false},
		{name: "no scopes", scopes: nil, perm: PermConfigRead, res: cfgRes("x"), want: false},
		{name: "unknown permission", scopes: []string{"admin"}, perm: "site:delete", res: resShop, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := token(t, tt.scopes...)
			require.Equal(t, tt.want, p.Can(tt.perm, tt.res))
			err := p.Require(tt.perm, tt.res)
			if tt.want {
				require.NoError(t, err)
				return
			}
			require.Equal(t, apperr.ReasonScopeMissing, apperr.ReasonOf(err))
		})
	}
}

func TestTokenAdminScope(t *testing.T) {
	t.Parallel()
	p := token(t, "admin")
	for _, perm := range AllPermissions() {
		want := RoleHas(RoleAdmin, perm)
		for _, res := range []Resource{resProd, resShop, resMarket, cfgRes("any"), secRes("db/pw")} {
			require.Equal(t, want, p.Can(perm, res), "%s on %+v", perm, res)
		}
	}
	for _, perm := range []Permission{
		PermTenantManage, PermKEKManage, PermUserRead, PermUserWrite, PermNamespaceWrite,
		PermLeaseAcquire, PermReportWrite, PermSecretRead,
	} {
		require.False(t, p.Can(perm, resShop), perm)
	}
	for _, perm := range []Permission{PermSecretReveal, PermIdentityReveal, PermTokenRead, PermTokenWrite, PermNamespaceRead} {
		require.True(t, p.Can(perm, resProd), perm)
	}
}

// TestTokenNonAdminScopesNeverGrantSensitive enumerates every non-admin scope
// shape and asserts the sensitive permissions are never reachable.
func TestTokenNonAdminScopesNeverGrantSensitive(t *testing.T) {
	t.Parallel()
	scopes := []string{
		"lease:acquire", "lease:acquire:shop", "report:write", "report:write:shop",
		"config:read", "config:read:*", "config:publish", "config:publish:*",
		"secret:read:*", "identity:write", "identity:write:shop", "proxy:write",
	}
	sensitive := []Permission{
		PermIdentityReveal, PermSecretReveal, PermTokenRead, PermTokenWrite, PermUserRead, PermUserWrite,
		PermNamespaceRead, PermNamespaceWrite, PermTenantManage, PermKEKManage, PermSiteWrite,
		PermPolicyPublish, PermSecretWrite, PermSecretList, PermAuditRead, PermNotifyWrite,
	}
	p := token(t, scopes...)
	resources := []Resource{resProd, resShop, cfgRes("g"), secRes("db/pw"), resTenantA}
	for _, perm := range sensitive {
		for _, res := range resources {
			require.False(t, p.Can(perm, res), "%s on %+v", perm, res)
		}
	}
}

func TestTokenPrincipalBinding(t *testing.T) {
	t.Parallel()
	scopes, err := ParseScopes([]string{"admin", "lease:acquire"})
	require.NoError(t, err)
	tests := []struct {
		name string
		p    *Principal
	}{
		{name: "missing namespace", p: &Principal{Kind: KindToken, TenantID: tenantA, Scopes: scopes}},
		{name: "missing tenant", p: &Principal{Kind: KindToken, NamespaceID: nsProd, Scopes: scopes}},
		{name: "missing both", p: &Principal{Kind: KindToken, Scopes: scopes}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			for _, res := range []Resource{resProd, resShop, {NamespaceID: nsProd}, {TenantID: tenantA}, {}} {
				require.False(t, tt.p.Can(PermSiteRead, res), "%+v", res)
				require.False(t, tt.p.Can(PermLeaseAcquire, res), "%+v", res)
			}
			all, ids := tt.p.SiteFilter(tenantA, nsProd, PermSiteRead)
			require.False(t, all)
			require.Nil(t, ids)
		})
	}
}

func TestTokenHandBuiltInvalidScopes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		scope Scope
		perm  Permission
		res   Resource
	}{
		{scope: Scope{Name: ScopeProxyWrite, Arg: "x"}, perm: PermProxyRead, res: resProd},
		{scope: Scope{Name: ScopeAdmin, Arg: "x"}, perm: PermSiteRead, res: resProd},
		{scope: Scope{Name: ScopeSecretRead}, perm: PermSecretRead, res: secRes("db/pw")},
		{scope: Scope{Name: "bogus"}, perm: PermSiteRead, res: resProd},
		{scope: Scope{Raw: "admin"}, perm: PermSiteRead, res: resProd},
		{scope: Scope{Raw: "lease:acquire", Name: ScopeLeaseAcquire, Arg: "shop"}, perm: PermLeaseAcquire, res: resMarket},
	}
	for i, tt := range tests {
		p := &Principal{Kind: KindToken, TenantID: tenantA, NamespaceID: nsProd, NamespaceName: nsProdName, Scopes: []Scope{tt.scope}}
		name := fmt.Sprintf("%d %+v", i, tt.scope)
		require.False(t, p.Can(tt.perm, tt.res), name)
		all, ids := p.SiteFilter(tenantA, nsProd, tt.perm)
		require.False(t, all, name)
		require.Nil(t, ids, name)
	}
}

func TestTokenSecretReadWithoutPrincipalNamespaceName(t *testing.T) {
	t.Parallel()
	p := token(t, "secret:read:prod/*")
	p.NamespaceName = ""
	require.True(t, p.Can(PermSecretRead, secRes("db/pw")), "resource namespace name is used")
	res := secRes("db/pw")
	res.NamespaceName = ""
	require.False(t, p.Can(PermSecretRead, res), "no namespace name anywhere must deny")
}

func TestTokenScopeArgumentsAreNotPatternsForSites(t *testing.T) {
	t.Parallel()
	// A hand-built site scope with a wildcard (ParseScope rejects it) must not
	// behave as a glob.
	p := &Principal{Kind: KindToken, TenantID: tenantA, NamespaceID: nsProd,
		Scopes: []Scope{{Raw: "lease:acquire:*", Name: ScopeLeaseAcquire, Arg: "*"}}}
	require.False(t, p.Can(PermLeaseAcquire, resShop))
	require.True(t, strings.HasPrefix(p.Scopes[0].Raw, ScopeLeaseAcquire))
}
