package authz

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Expected role permission sets, written out literally from spec §3.2 so that
// any drift in permission.go is caught.
var (
	wantViewer = []Permission{
		"namespace:read", "site:read", "identity:read", "proxy:read", "policy:read", "breaker:read",
		"config:read", "secret:list", "audit:read", "notify:read", "dashboard:read",
	}
	wantOperator = append(clonePerms(wantViewer),
		"identity:write", "identity:operate", "proxy:write", "proxy:operate", "policy:write", "config:write", "breaker:operate",
	)
	wantAdmin = append(clonePerms(wantOperator),
		"site:write", "policy:publish", "config:publish", "secret:write", "secret:reveal", "identity:reveal",
		"token:read", "token:write", "notify:write", "namespace:read",
	)
	wantOwner = append(clonePerms(wantAdmin), "namespace:write", "user:read", "user:write")
)

func clonePerms(p []Permission) []Permission { return append([]Permission(nil), p...) }

func uniquePerms(p []Permission) []Permission {
	seen := map[Permission]bool{}
	var out []Permission
	for _, x := range p {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func TestAllPermissions(t *testing.T) {
	t.Parallel()
	want := []Permission{
		"tenant:manage", "kek:manage",
		"namespace:read", "namespace:write",
		"site:read", "site:write",
		"identity:read", "identity:write", "identity:operate", "identity:reveal",
		"proxy:read", "proxy:write", "proxy:operate",
		"policy:read", "policy:write", "policy:publish",
		"breaker:read", "breaker:operate",
		"config:read", "config:write", "config:publish",
		"secret:list", "secret:write", "secret:reveal",
		"token:read", "token:write",
		"user:read", "user:write",
		"audit:read",
		"notify:read", "notify:write",
		"dashboard:read",
		"lease:acquire", "report:write", "secret:read",
	}
	got := AllPermissions()
	require.Equal(t, want, got)
	require.Len(t, uniquePerms(got), len(got), "permissions must be unique")

	got[0] = "mutated"
	require.Equal(t, PermTenantManage, AllPermissions()[0], "AllPermissions must return a copy")

	for _, p := range want {
		require.True(t, ValidPermission(p), p)
		parts := strings.Split(string(p), ":")
		require.Len(t, parts, 2, p)
	}
	for _, p := range []Permission{"", "tenant", "tenant:*", "*", "admin", "SITE:READ", "site:read "} {
		require.False(t, ValidPermission(p), p)
	}
}

func TestPlatformAndNodeOnly(t *testing.T) {
	t.Parallel()
	for _, p := range AllPermissions() {
		require.Equal(t, p == PermTenantManage || p == PermKEKManage, PlatformOnly(p), p)
		require.Equal(t, p == PermLeaseAcquire || p == PermReportWrite || p == PermSecretRead, NodeOnly(p), p)
	}
}

func TestRoles(t *testing.T) {
	t.Parallel()
	require.Equal(t, []Role{RoleViewer, RoleOperator, RoleAdmin, RoleOwner}, Roles())
	for _, r := range Roles() {
		require.True(t, ValidRole(r), r)
	}
	for _, r := range []Role{"", "Owner", "superuser", "platform_admin", "viewer "} {
		require.False(t, ValidRole(r), r)
		require.Nil(t, RolePermissions(r), r)
	}
}

func TestRolePermissionsExact(t *testing.T) {
	t.Parallel()
	tests := []struct {
		role Role
		want []Permission
	}{
		{role: RoleViewer, want: wantViewer},
		{role: RoleOperator, want: wantOperator},
		{role: RoleAdmin, want: uniquePerms(wantAdmin)},
		{role: RoleOwner, want: uniquePerms(wantOwner)},
	}
	for _, tt := range tests {
		t.Run(string(tt.role), func(t *testing.T) {
			t.Parallel()
			got := RolePermissions(tt.role)
			require.ElementsMatch(t, tt.want, got)
			require.Len(t, uniquePerms(got), len(got))
			for _, p := range AllPermissions() {
				require.Equal(t, containsPerm(tt.want, p), RoleHas(tt.role, p), "%s %s", tt.role, p)
			}
			// No role ever holds platform-only or node-only permissions.
			for _, p := range got {
				require.False(t, PlatformOnly(p), p)
				require.False(t, NodeOnly(p), p)
			}
		})
	}
}

func TestRolePermissionsHierarchy(t *testing.T) {
	t.Parallel()
	roles := Roles()
	for i := 1; i < len(roles); i++ {
		lower, higher := RolePermissions(roles[i-1]), RolePermissions(roles[i])
		require.Greater(t, len(higher), len(lower))
		for _, p := range lower {
			require.True(t, RoleHas(roles[i], p), "%s must include %s from %s", roles[i], p, roles[i-1])
		}
	}
	// Specific boundaries from the spec.
	require.False(t, RoleHas(RoleViewer, PermSecretReveal), "viewer must never reveal secrets")
	require.False(t, RoleHas(RoleViewer, PermTokenRead))
	require.False(t, RoleHas(RoleViewer, PermUserRead))
	require.False(t, RoleHas(RoleOperator, PermConfigPublish))
	require.False(t, RoleHas(RoleOperator, PermPolicyPublish))
	require.False(t, RoleHas(RoleOperator, PermSiteWrite))
	require.False(t, RoleHas(RoleAdmin, PermNamespaceWrite))
	require.False(t, RoleHas(RoleAdmin, PermUserWrite))
	require.False(t, RoleHas(RoleAdmin, PermUserRead))
	require.True(t, RoleHas(RoleOwner, PermUserWrite))
	require.False(t, RoleHas("bogus", PermSiteRead))
}

func TestRolePermissionsReturnsCopy(t *testing.T) {
	t.Parallel()
	got := RolePermissions(RoleViewer)
	got[0] = PermTenantManage
	require.False(t, RoleHas(RoleViewer, PermTenantManage))
	require.NotEqual(t, PermTenantManage, RolePermissions(RoleViewer)[0])
}

func TestValidExtraPermission(t *testing.T) {
	t.Parallel()
	allowed := map[Permission]bool{
		PermConfigPublish: true, PermSecretReveal: true, PermIdentityReveal: true, PermPolicyPublish: true,
	}
	for _, p := range AllPermissions() {
		require.Equal(t, allowed[p], ValidExtraPermission(p), p)
	}
	require.False(t, ValidExtraPermission(""))
	require.False(t, ValidExtraPermission("config:publish:*"))
}

func containsPerm(list []Permission, p Permission) bool {
	for _, x := range list {
		if x == p {
			return true
		}
	}
	return false
}
