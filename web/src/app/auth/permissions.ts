import { type NamespaceAccess, type RoleBinding, type TenantAccess } from '@/gen/spinneret/v1/auth_pb';

/** Permission strings of implementation spec section 3.2. */
export const PERMISSIONS = {
  tenantManage: 'tenant:manage',
  kekManage: 'kek:manage',
  namespaceRead: 'namespace:read',
  namespaceWrite: 'namespace:write',
  siteRead: 'site:read',
  siteWrite: 'site:write',
  identityRead: 'identity:read',
  identityWrite: 'identity:write',
  identityOperate: 'identity:operate',
  identityReveal: 'identity:reveal',
  proxyRead: 'proxy:read',
  proxyWrite: 'proxy:write',
  proxyOperate: 'proxy:operate',
  policyRead: 'policy:read',
  policyWrite: 'policy:write',
  policyPublish: 'policy:publish',
  breakerRead: 'breaker:read',
  breakerOperate: 'breaker:operate',
  configRead: 'config:read',
  configWrite: 'config:write',
  configPublish: 'config:publish',
  secretList: 'secret:list',
  secretWrite: 'secret:write',
  secretReveal: 'secret:reveal',
  tokenRead: 'token:read',
  tokenWrite: 'token:write',
  userRead: 'user:read',
  userWrite: 'user:write',
  auditRead: 'audit:read',
  notifyRead: 'notify:read',
  notifyWrite: 'notify:write',
  dashboardRead: 'dashboard:read',
} as const;

export type Permission = (typeof PERMISSIONS)[keyof typeof PERMISSIONS];

/** Granted only to platform administrators. */
export const PLATFORM_PERMISSIONS: ReadonlySet<string> = new Set([
  PERMISSIONS.tenantManage,
  PERMISSIONS.kekManage,
]);

/**
 * Namespace-level (and tenant-level) permissions: they require a binding
 * without site restriction, so per-site grants never satisfy them. proxy:read
 * and namespace:read are listed separately (SITE_RESTRICTED_NAMESPACE_PERMISSIONS).
 */
export const NAMESPACE_LEVEL_PERMISSIONS: ReadonlySet<string> = new Set([
  PERMISSIONS.namespaceWrite,
  PERMISSIONS.proxyWrite,
  PERMISSIONS.proxyOperate,
  PERMISSIONS.configRead,
  PERMISSIONS.configWrite,
  PERMISSIONS.configPublish,
  PERMISSIONS.secretList,
  PERMISSIONS.secretWrite,
  PERMISSIONS.secretReveal,
  PERMISSIONS.tokenRead,
  PERMISSIONS.tokenWrite,
  PERMISSIONS.userRead,
  PERMISSIONS.userWrite,
  PERMISSIONS.auditRead,
  PERMISSIONS.notifyRead,
  PERMISSIONS.notifyWrite,
]);

/**
 * Namespace-level read permissions that site-restricted bindings also hold,
 * but only when the binding is pinned to that namespace (internal/authz
 * Binding.allows).
 */
export const SITE_RESTRICTED_NAMESPACE_PERMISSIONS: ReadonlySet<string> = new Set([
  PERMISSIONS.proxyRead,
  PERMISSIONS.namespaceRead,
]);

const VIEWER_PERMISSIONS = [
  PERMISSIONS.namespaceRead,
  PERMISSIONS.siteRead,
  PERMISSIONS.identityRead,
  PERMISSIONS.proxyRead,
  PERMISSIONS.policyRead,
  PERMISSIONS.breakerRead,
  PERMISSIONS.configRead,
  PERMISSIONS.secretList,
  PERMISSIONS.dashboardRead,
  PERMISSIONS.auditRead,
  PERMISSIONS.notifyRead,
];
const OPERATOR_PERMISSIONS = [
  ...VIEWER_PERMISSIONS,
  PERMISSIONS.identityWrite,
  PERMISSIONS.identityOperate,
  PERMISSIONS.proxyWrite,
  PERMISSIONS.proxyOperate,
  PERMISSIONS.policyWrite,
  PERMISSIONS.configWrite,
  PERMISSIONS.breakerOperate,
];
const ADMIN_PERMISSIONS = [
  ...OPERATOR_PERMISSIONS,
  PERMISSIONS.siteWrite,
  PERMISSIONS.policyPublish,
  PERMISSIONS.configPublish,
  PERMISSIONS.secretWrite,
  PERMISSIONS.secretReveal,
  PERMISSIONS.identityReveal,
  PERMISSIONS.tokenRead,
  PERMISSIONS.tokenWrite,
  PERMISSIONS.notifyWrite,
];
const OWNER_PERMISSIONS = [
  ...ADMIN_PERMISSIONS,
  PERMISSIONS.namespaceWrite,
  PERMISSIONS.userRead,
  PERMISSIONS.userWrite,
];

/** Role permission sets (implementation spec section 3.2, mirrors internal/authz). */
export const ROLE_PERMISSIONS: Readonly<Record<string, ReadonlySet<string>>> = {
  viewer: new Set(VIEWER_PERMISSIONS),
  operator: new Set(OPERATOR_PERMISSIONS),
  admin: new Set(ADMIN_PERMISSIONS),
  owner: new Set(OWNER_PERMISSIONS),
};

/** Permissions a role binding may add on top of its role. */
export const EXTRA_PERMISSIONS: ReadonlySet<string> = new Set([
  PERMISSIONS.configPublish,
  PERMISSIONS.secretReveal,
  PERMISSIONS.identityReveal,
  PERMISSIONS.policyPublish,
]);

/** Reports whether a binding's role or valid extra permissions include the permission. */
export function bindingGrants(binding: RoleBinding, permission: string): boolean {
  const role = ROLE_PERMISSIONS[binding.role];
  if (!role) return false;
  if (role.has(permission)) return true;
  return EXTRA_PERMISSIONS.has(permission) && binding.extraPermissions.includes(permission);
}

/** Inputs of a permission check. */
export interface PermissionScope {
  isPlatformAdmin: boolean;
  /** Effective access in the active namespace (undefined when none is selected). */
  namespace: NamespaceAccess | undefined;
  /**
   * Role bindings of the user in the active tenant. When present they refine
   * proxy:read / namespace:read granted through site-restricted bindings.
   */
  bindings?: readonly RoleBinding[];
}

/**
 * Reports whether a permission is granted in the active namespace.
 *
 * - Platform admins pass every check.
 * - tenant:manage and kek:manage are platform-admin only.
 * - Namespace-wide grants satisfy every check.
 * - proxy:read and namespace:read also come from site-restricted bindings
 *   pinned to the namespace.
 * - With `site` (site ID or site name), per-site grants of that site count.
 * - Without `site`, a per-site grant on any site counts ("can do this somewhere"),
 *   which drives navigation visibility; namespace-level permissions never
 *   come from per-site grants.
 *
 * Tenant-level resources (users, role bindings, tenant-wide notification
 * channels) are checked with checkTenantPermission instead.
 */
export function checkPermission(scope: PermissionScope, permission: string, site?: string): boolean {
  if (scope.isPlatformAdmin) return true;
  if (PLATFORM_PERMISSIONS.has(permission)) return false;
  const ns = scope.namespace;
  if (!ns) return false;
  if (ns.permissions.includes(permission)) return true;
  if (SITE_RESTRICTED_NAMESPACE_PERMISSIONS.has(permission)) {
    const namespaceId = ns.namespace?.id;
    if (scope.bindings && namespaceId) {
      return scope.bindings.some(
        (b) => b.namespaceId === namespaceId && b.siteIds.length > 0 && bindingGrants(b, permission),
      );
    }
    return ns.sites.some((s) => s.permissions.includes(permission));
  }
  if (NAMESPACE_LEVEL_PERMISSIONS.has(permission)) return false;
  if (site !== undefined && site !== '') {
    return ns.sites.some(
      (s) => (s.siteId === site || s.siteName === site) && s.permissions.includes(permission),
    );
  }
  return ns.sites.some((s) => s.permissions.includes(permission));
}

/** Inputs of a tenant-level permission check. */
export interface TenantPermissionScope {
  isPlatformAdmin: boolean;
  /** The active tenant (undefined when none is selected). */
  tenant: TenantAccess | undefined;
}

/**
 * Reports whether a permission is granted on tenant-level resources (users,
 * role bindings, tenant-wide notification channels and alerts): it requires a
 * binding covering every namespace and every site of the active tenant.
 * Platform admins pass every check.
 */
export function checkTenantPermission(scope: TenantPermissionScope, permission: string): boolean {
  if (scope.isPlatformAdmin) return true;
  if (PLATFORM_PERMISSIONS.has(permission) || !scope.tenant) return false;
  return scope.tenant.bindings.some(
    (b) => b.namespaceId === '' && b.siteIds.length === 0 && bindingGrants(b, permission),
  );
}

/** Site IDs where the permission is granted only per site (empty when namespace-wide or none). */
export function sitesWithPermission(scope: PermissionScope, permission: string): string[] {
  const ns = scope.namespace;
  if (!ns || scope.isPlatformAdmin || ns.permissions.includes(permission)) return [];
  return ns.sites.filter((s) => s.permissions.includes(permission)).map((s) => s.siteId);
}

/** Selected tenant and namespace derived from GetMe and the stored preference. */
export interface ResolvedSelection {
  tenant: TenantAccess | undefined;
  namespace: NamespaceAccess | undefined;
}

export const DEFAULT_NAMESPACE_NAME = 'default';

/**
 * Picks the active tenant (stored ID when still accessible, else the first) and
 * namespace (stored name when present in that tenant, else "default", else the first).
 */
export function resolveSelection(
  tenants: readonly TenantAccess[],
  storedTenantId: string | undefined,
  storedNamespace: string | undefined,
): ResolvedSelection {
  const tenant = tenants.find((t) => t.tenant?.id === storedTenantId) ?? tenants[0];
  if (!tenant) return { tenant: undefined, namespace: undefined };
  const namespaces = tenant.namespaces;
  const namespace =
    namespaces.find((n) => n.namespace?.name === storedNamespace) ??
    namespaces.find((n) => n.namespace?.name === DEFAULT_NAMESPACE_NAME) ??
    namespaces[0];
  return { tenant, namespace };
}
