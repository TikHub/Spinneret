package authz

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
)

func TestSiteFilterUsers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		p        *Principal
		tenant   string
		ns       string
		perm     Permission
		wantAll  bool
		wantSite []string
	}{
		{name: "tenant-wide binding", p: user(Binding{TenantID: tenantA, Role: RoleViewer}),
			tenant: tenantA, ns: nsProd, perm: PermIdentityRead, wantAll: true},
		{name: "namespace binding match", p: user(Binding{TenantID: tenantA, Role: RoleViewer, NamespaceID: nsProd}),
			tenant: tenantA, ns: nsProd, perm: PermIdentityRead, wantAll: true},
		{name: "namespace binding other namespace", p: user(Binding{TenantID: tenantA, Role: RoleViewer, NamespaceID: nsProd}),
			tenant: tenantA, ns: nsStaging, perm: PermIdentityRead},
		{name: "role lacks permission", p: user(Binding{TenantID: tenantA, Role: RoleViewer}),
			tenant: tenantA, ns: nsProd, perm: PermIdentityWrite},
		{name: "site restricted", p: user(Binding{TenantID: tenantA, Role: RoleOperator, NamespaceID: nsProd, SiteIDs: []string{siteMarket, siteShop}}),
			tenant: tenantA, ns: nsProd, perm: PermIdentityWrite, wantSite: []string{siteMarket, siteShop}},
		{name: "site restricted merged and deduplicated", p: user(
			Binding{TenantID: tenantA, Role: RoleOperator, NamespaceID: nsProd, SiteIDs: []string{siteMarket, siteShop}},
			Binding{TenantID: tenantA, Role: RoleViewer, SiteIDs: []string{siteShop, "", siteBlog}},
		), tenant: tenantA, ns: nsProd, perm: PermIdentityRead, wantSite: []string{siteBlog, siteMarket, siteShop}},
		{name: "unrestricted binding wins over restricted", p: user(
			Binding{TenantID: tenantA, Role: RoleOperator, NamespaceID: nsProd, SiteIDs: []string{siteShop}},
			Binding{TenantID: tenantA, Role: RoleViewer, NamespaceID: nsProd},
		), tenant: tenantA, ns: nsProd, perm: PermIdentityRead, wantAll: true},
		{name: "restricted binding for other permission ignored", p: user(
			Binding{TenantID: tenantA, Role: RoleOperator, NamespaceID: nsProd, SiteIDs: []string{siteShop}},
			Binding{TenantID: tenantA, Role: RoleViewer, NamespaceID: nsProd, SiteIDs: []string{siteMarket}},
		), tenant: tenantA, ns: nsProd, perm: PermIdentityWrite, wantSite: []string{siteShop}},
		{name: "extra permission counts", p: user(Binding{TenantID: tenantA, Role: RoleViewer, NamespaceID: nsProd, SiteIDs: []string{siteShop}, Extra: []Permission{PermIdentityReveal}}),
			tenant: tenantA, ns: nsProd, perm: PermIdentityReveal, wantSite: []string{siteShop}},
		{name: "other tenant", p: user(Binding{TenantID: tenantB, Role: RoleOwner}),
			tenant: tenantA, ns: nsProd, perm: PermSiteRead},
		{name: "empty tenant", p: user(Binding{TenantID: tenantA, Role: RoleOwner}),
			tenant: "", ns: nsProd, perm: PermSiteRead},
		{name: "empty namespace tenant-wide binding", p: user(Binding{TenantID: tenantA, Role: RoleOwner}),
			tenant: tenantA, ns: "", perm: PermSiteRead, wantAll: true},
		{name: "empty namespace restricted binding", p: user(Binding{TenantID: tenantA, Role: RoleOwner, SiteIDs: []string{siteShop}}),
			tenant: tenantA, ns: "", perm: PermSiteRead},
		{name: "platform only permission", p: user(Binding{TenantID: tenantA, Role: RoleOwner}),
			tenant: tenantA, ns: nsProd, perm: PermTenantManage},
		{name: "unknown permission", p: user(Binding{TenantID: tenantA, Role: RoleOwner}),
			tenant: tenantA, ns: nsProd, perm: "bogus"},
		{name: "platform admin", p: &Principal{Kind: KindUser, IsPlatformAdmin: true},
			tenant: tenantB, ns: nsOther, perm: PermKEKManage, wantAll: true},
		{name: "system", p: System("jobs"), tenant: "", ns: "", perm: PermSiteRead, wantAll: true},
		{name: "nil principal", p: nil, tenant: tenantA, ns: nsProd, perm: PermSiteRead},
		{name: "unknown kind", p: &Principal{Kind: "robot", IsPlatformAdmin: true}, tenant: tenantA, ns: nsProd, perm: PermSiteRead},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			all, ids := tt.p.SiteFilter(tt.tenant, tt.ns, tt.perm)
			require.Equal(t, tt.wantAll, all)
			require.Equal(t, tt.wantSite, ids)
		})
	}
}

// TestSiteFilterConsistentWithCan asserts, for users, that a site is
// accessible through Can exactly when SiteFilter reports all or lists it.
func TestSiteFilterConsistentWithCan(t *testing.T) {
	t.Parallel()
	sites := []string{siteShop, siteMarket, siteBlog, "sit_unknown"}
	principals := []*Principal{
		user(),
		user(Binding{TenantID: tenantA, Role: RoleViewer}),
		user(Binding{TenantID: tenantA, Role: RoleOperator, NamespaceID: nsProd, SiteIDs: []string{siteShop}}),
		user(Binding{TenantID: tenantA, Role: RoleAdmin, SiteIDs: []string{siteMarket, siteBlog}},
			Binding{TenantID: tenantA, Role: RoleViewer, NamespaceID: nsStaging}),
		user(Binding{TenantID: tenantB, Role: RoleOwner},
			Binding{TenantID: tenantA, Role: RoleViewer, NamespaceID: nsProd, SiteIDs: []string{siteBlog}, Extra: []Permission{PermSecretReveal}}),
		{Kind: KindUser, IsPlatformAdmin: true},
		System("x"),
	}
	for pi, p := range principals {
		for _, ns := range []string{nsProd, nsStaging} {
			for _, perm := range AllPermissions() {
				all, ids := p.SiteFilter(tenantA, ns, perm)
				for _, site := range sites {
					want := all || containsString(ids, site)
					got := p.Can(perm, Resource{TenantID: tenantA, NamespaceID: ns, SiteID: site})
					require.Equal(t, want, got, "principal %d ns %s perm %s site %s", pi, ns, perm, site)
				}
			}
		}
	}
}

func TestSiteFilterTokens(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		scopes  []string
		tenant  string
		ns      string
		perm    Permission
		wantAll bool
	}{
		{name: "unrestricted lease", scopes: []string{"lease:acquire"}, tenant: tenantA, ns: nsProd, perm: PermLeaseAcquire, wantAll: true},
		{name: "site-named lease", scopes: []string{"lease:acquire:shop"}, tenant: tenantA, ns: nsProd, perm: PermLeaseAcquire},
		{name: "mixed site-named and unrestricted", scopes: []string{"identity:write:shop", "identity:write"}, tenant: tenantA, ns: nsProd, perm: PermIdentityRead, wantAll: true},
		{name: "config glob is not a site restriction", scopes: []string{"config:read:app*"}, tenant: tenantA, ns: nsProd, perm: PermConfigRead, wantAll: true},
		{name: "secret glob is not a site restriction", scopes: []string{"secret:read:prod/*"}, tenant: tenantA, ns: nsProd, perm: PermSecretRead, wantAll: true},
		{name: "admin", scopes: []string{"admin"}, tenant: tenantA, ns: nsProd, perm: PermIdentityReveal, wantAll: true},
		{name: "admin lacks owner permission", scopes: []string{"admin"}, tenant: tenantA, ns: nsProd, perm: PermUserRead},
		{name: "scope lacks permission", scopes: []string{"lease:acquire"}, tenant: tenantA, ns: nsProd, perm: PermReportWrite},
		{name: "other namespace", scopes: []string{"admin"}, tenant: tenantA, ns: nsStaging, perm: PermSiteRead},
		{name: "other tenant", scopes: []string{"admin"}, tenant: tenantB, ns: nsProd, perm: PermSiteRead},
		{name: "empty namespace", scopes: []string{"admin"}, tenant: tenantA, ns: "", perm: PermSiteRead},
		{name: "platform only", scopes: []string{"admin"}, tenant: tenantA, ns: nsProd, perm: PermTenantManage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			all, ids := token(t, tt.scopes...).SiteFilter(tt.tenant, tt.ns, tt.perm)
			require.Equal(t, tt.wantAll, all)
			require.Nil(t, ids)
		})
	}
}

func TestTenantIDs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		p    *Principal
		want []string
	}{
		{name: "nil principal", p: nil, want: []string{}},
		{name: "system", p: System("x"), want: nil},
		{name: "platform admin", p: &Principal{Kind: KindUser, IsPlatformAdmin: true}, want: nil},
		{name: "user without bindings", p: user(), want: []string{}},
		{name: "user sorted unique", p: user(
			Binding{TenantID: tenantB, Role: RoleViewer},
			Binding{TenantID: tenantA, Role: RoleOwner, NamespaceID: nsProd},
			Binding{TenantID: tenantA, Role: RoleViewer},
			Binding{TenantID: "", Role: RoleViewer},
			Binding{TenantID: "ten_c", Role: "bogus"},
		), want: []string{tenantA, tenantB}},
		{name: "token", p: token(t, "admin"), want: []string{tenantA}},
		{name: "token without tenant", p: &Principal{Kind: KindToken}, want: []string{}},
		{name: "unknown kind", p: &Principal{Kind: "robot", TenantID: tenantA}, want: []string{}},
	}
	for _, tt := range tests {
		got := tt.p.TenantIDs()
		require.Equal(t, tt.want, got, tt.name)
		if tt.want != nil {
			require.NotNil(t, got, "%s: non-admin principals must return a non-nil slice", tt.name)
		}
	}
}

func TestActor(t *testing.T) {
	t.Parallel()
	var nilP *Principal
	require.Equal(t, "anonymous", nilP.Actor())
	require.Equal(t, "user:usr_1", user().Actor())
	require.Equal(t, "token:tok_1", token(t).Actor())
	require.Equal(t, "system", System("rewrap").Actor())
	require.Equal(t, "anonymous", (&Principal{Kind: "robot", ID: "x"}).Actor())
}

func TestSystem(t *testing.T) {
	t.Parallel()
	p := System("breaker-evaluator")
	require.Equal(t, KindSystem, p.Kind)
	require.Equal(t, "system", p.ID)
	require.Equal(t, "breaker-evaluator", p.Name)
	require.True(t, p.IsPlatformAdmin)
	require.Equal(t, "system", System("").Name)
	require.NotSame(t, System("a"), System("a"), "System returns a fresh principal")
	for _, perm := range AllPermissions() {
		require.True(t, p.Can(perm, Resource{}), perm)
	}
	require.False(t, p.Can("bogus", Resource{}))
}

func TestNilPrincipal(t *testing.T) {
	t.Parallel()
	var p *Principal
	for _, perm := range AllPermissions() {
		require.False(t, p.Can(perm, resShop))
	}
}

func TestContextHelpers(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	_, ok := FromContext(ctx)
	require.False(t, ok)
	_, err := MustPrincipal(ctx)
	ae, isApp := apperr.As(err)
	require.True(t, isApp)
	require.Equal(t, connect.CodeUnauthenticated, ae.Code)
	require.Equal(t, apperr.ReasonSessionInvalid, ae.Reason)

	p := user(Binding{TenantID: tenantA, Role: RoleViewer})
	ctx2 := WithPrincipal(ctx, p)
	got, ok := FromContext(ctx2)
	require.True(t, ok)
	require.Same(t, p, got)
	got, err = MustPrincipal(ctx2)
	require.NoError(t, err)
	require.Same(t, p, got)

	// Storing nil behaves like no principal.
	ctx3 := WithPrincipal(ctx2, nil)
	_, ok = FromContext(ctx3)
	require.False(t, ok)
	_, err = MustPrincipal(ctx3)
	require.Error(t, err)

	// A nil context is tolerated.
	var nilCtx context.Context
	_, ok = FromContext(nilCtx)
	require.False(t, ok)
	ctx4 := WithPrincipal(nilCtx, p)
	got, ok = FromContext(ctx4)
	require.True(t, ok)
	require.Same(t, p, got)

	// Unrelated values under other keys are not confused with principals.
	type otherKey struct{}
	ctx5 := context.WithValue(ctx, otherKey{}, p)
	_, ok = FromContext(ctx5)
	require.False(t, ok)
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
