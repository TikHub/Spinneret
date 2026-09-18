// Package authz implements Spinneret's authorization model: permissions,
// console roles, API token scopes, principals and resource-scoped permission
// checks (spec §3.2). The package is pure — it performs no I/O — so every
// decision is a deterministic function of the Principal and the Resource.
//
// Every check fails closed: unknown permissions, roles, scopes or principal
// kinds, and malformed resources are denied.
package authz

import "slices"

// Permission is a "<resource>:<verb>" permission string.
type Permission string

// Permissions (spec §3.2).
const (
	// Platform-admin-only permissions.
	PermTenantManage Permission = "tenant:manage"
	PermKEKManage    Permission = "kek:manage"

	PermNamespaceRead  Permission = "namespace:read"
	PermNamespaceWrite Permission = "namespace:write"

	PermSiteRead  Permission = "site:read"
	PermSiteWrite Permission = "site:write"

	PermIdentityRead    Permission = "identity:read"
	PermIdentityWrite   Permission = "identity:write"
	PermIdentityOperate Permission = "identity:operate"
	PermIdentityReveal  Permission = "identity:reveal"

	PermProxyRead    Permission = "proxy:read"
	PermProxyWrite   Permission = "proxy:write"
	PermProxyOperate Permission = "proxy:operate"

	PermPolicyRead    Permission = "policy:read"
	PermPolicyWrite   Permission = "policy:write"
	PermPolicyPublish Permission = "policy:publish"

	PermBreakerRead    Permission = "breaker:read"
	PermBreakerOperate Permission = "breaker:operate"

	PermConfigRead    Permission = "config:read"
	PermConfigWrite   Permission = "config:write"
	PermConfigPublish Permission = "config:publish"

	PermSecretList   Permission = "secret:list"
	PermSecretWrite  Permission = "secret:write"
	PermSecretReveal Permission = "secret:reveal"

	PermTokenRead  Permission = "token:read"
	PermTokenWrite Permission = "token:write"

	PermUserRead  Permission = "user:read"
	PermUserWrite Permission = "user:write"

	PermAuditRead Permission = "audit:read"

	PermNotifyRead  Permission = "notify:read"
	PermNotifyWrite Permission = "notify:write"

	PermDashboardRead Permission = "dashboard:read"

	// Node-only permissions, granted exclusively through API token scopes.
	PermLeaseAcquire Permission = "lease:acquire"
	PermReportWrite  Permission = "report:write"
	PermSecretRead   Permission = "secret:read"
)

// Role is a console role bound to a user within a tenant.
type Role string

// Roles, from least to most privileged.
const (
	RoleOwner    Role = "owner"
	RoleAdmin    Role = "admin"
	RoleOperator Role = "operator"
	RoleViewer   Role = "viewer"
)

// allPermissions lists every permission in canonical order. Read-only.
var allPermissions = []Permission{
	PermTenantManage, PermKEKManage,
	PermNamespaceRead, PermNamespaceWrite,
	PermSiteRead, PermSiteWrite,
	PermIdentityRead, PermIdentityWrite, PermIdentityOperate, PermIdentityReveal,
	PermProxyRead, PermProxyWrite, PermProxyOperate,
	PermPolicyRead, PermPolicyWrite, PermPolicyPublish,
	PermBreakerRead, PermBreakerOperate,
	PermConfigRead, PermConfigWrite, PermConfigPublish,
	PermSecretList, PermSecretWrite, PermSecretReveal,
	PermTokenRead, PermTokenWrite,
	PermUserRead, PermUserWrite,
	PermAuditRead,
	PermNotifyRead, PermNotifyWrite,
	PermDashboardRead,
	PermLeaseAcquire, PermReportWrite, PermSecretRead,
}

// Role permission sets (spec §3.2). viewer ⊂ operator ⊂ admin ⊂ owner.
//
// "viewer: all *:read" covers the console read permissions of namespace
// content (namespace, sites, identities, proxies, policies, breakers,
// configs). token:read and user:read are access-control data introduced
// explicitly by admin and owner, and secret:read is node-only, so they are not
// part of viewer.
var (
	viewerPermissions = []Permission{
		PermNamespaceRead, PermSiteRead, PermIdentityRead, PermProxyRead, PermPolicyRead,
		PermBreakerRead, PermConfigRead, PermSecretList, PermDashboardRead, PermAuditRead, PermNotifyRead,
	}
	operatorPermissions = concat(viewerPermissions,
		PermIdentityWrite, PermIdentityOperate, PermProxyWrite, PermProxyOperate,
		PermPolicyWrite, PermConfigWrite, PermBreakerOperate,
	)
	adminPermissions = concat(operatorPermissions,
		PermSiteWrite, PermPolicyPublish, PermConfigPublish, PermSecretWrite, PermSecretReveal,
		PermIdentityReveal, PermTokenRead, PermTokenWrite, PermNotifyWrite,
	)
	ownerPermissions = concat(adminPermissions,
		PermNamespaceWrite, PermUserRead, PermUserWrite,
	)

	rolePermissionSets = map[Role]map[Permission]struct{}{
		RoleViewer:   toSet(viewerPermissions),
		RoleOperator: toSet(operatorPermissions),
		RoleAdmin:    toSet(adminPermissions),
		RoleOwner:    toSet(ownerPermissions),
	}
)

// AllPermissions returns every known permission in canonical order.
func AllPermissions() []Permission {
	return slices.Clone(allPermissions)
}

// ValidPermission reports whether p is a known permission.
func ValidPermission(p Permission) bool {
	return slices.Contains(allPermissions, p)
}

// PlatformOnly reports whether p can only be held by platform admins (and the
// system principal): tenant:manage and kek:manage.
func PlatformOnly(p Permission) bool {
	return p == PermTenantManage || p == PermKEKManage
}

// NodeOnly reports whether p is granted exclusively through API token scopes:
// lease:acquire, report:write and secret:read.
func NodeOnly(p Permission) bool {
	return p == PermLeaseAcquire || p == PermReportWrite || p == PermSecretRead
}

// Roles returns every role from least to most privileged.
func Roles() []Role {
	return []Role{RoleViewer, RoleOperator, RoleAdmin, RoleOwner}
}

// ValidRole reports whether r is a known role.
func ValidRole(r Role) bool {
	_, ok := rolePermissionSets[r]
	return ok
}

// RolePermissions returns the permissions of role r in canonical order, or nil
// for an unknown role. The returned slice is a fresh copy.
func RolePermissions(r Role) []Permission {
	set, ok := rolePermissionSets[r]
	if !ok {
		return nil
	}
	out := make([]Permission, 0, len(set))
	for _, p := range allPermissions {
		if _, ok := set[p]; ok {
			out = append(out, p)
		}
	}
	return out
}

// RoleHas reports whether role r includes permission p.
func RoleHas(r Role, p Permission) bool {
	_, ok := rolePermissionSets[r][p]
	return ok
}

// ValidExtraPermission reports whether p may be granted to a role binding as
// an extra permission: config:publish, secret:reveal, identity:reveal or
// policy:publish.
func ValidExtraPermission(p Permission) bool {
	switch p {
	case PermConfigPublish, PermSecretReveal, PermIdentityReveal, PermPolicyPublish:
		return true
	default:
		return false
	}
}

func concat(base []Permission, extra ...Permission) []Permission {
	out := make([]Permission, 0, len(base)+len(extra))
	out = append(out, base...)
	return append(out, extra...)
}

func toSet(perms []Permission) map[Permission]struct{} {
	set := make(map[Permission]struct{}, len(perms))
	for _, p := range perms {
		set[p] = struct{}{}
	}
	return set
}
