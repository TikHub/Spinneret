/**
 * React Query key conventions. Every key starts with a domain (used by the
 * real-time event stream to invalidate by prefix), followed by the tenant ID
 * and namespace name, then page-specific parts:
 *
 *   [domain, tenantId, namespace, ...parts]
 *
 * Use `useScopedQueryKey()` (src/app/auth) to build keys for the active scope.
 */
export const QUERY_DOMAINS = [
  'auth',
  'dashboard',
  'identities',
  'identity-types',
  'accounts',
  'proxies',
  'sites',
  'policies',
  'heatmap',
  'breakers',
  'config',
  'secrets',
  'requests',
  'risk-events',
  'notifications',
  'access',
  'tenants',
] as const;

export type QueryDomain = (typeof QUERY_DOMAINS)[number];

/** Builds a scoped query key. */
export function scopedKey(
  domain: QueryDomain,
  tenantId: string | undefined,
  namespace: string | undefined,
  ...parts: unknown[]
): readonly unknown[] {
  return [domain, tenantId ?? '', namespace ?? '', ...parts];
}

/** Reports whether a scoped query key belongs to the given tenant and namespace. */
export function isKeyInScope(
  queryKey: readonly unknown[] | undefined,
  tenantId: string | undefined,
  namespace: string | undefined,
): boolean {
  return queryKey !== undefined && queryKey[1] === (tenantId ?? '') && queryKey[2] === (namespace ?? '');
}

/**
 * Builds a React Query `placeholderData` function that keeps the previous data
 * while a new page or filter loads (like keepPreviousData) but only within the
 * same tenant and namespace, so a scope switch never shows another scope's rows.
 */
export function keepPreviousInScope(tenantId: string | undefined, namespace: string | undefined) {
  return <T>(previousData: T | undefined, previousQuery?: { queryKey: readonly unknown[] }): T | undefined =>
    isKeyInScope(previousQuery?.queryKey, tenantId, namespace) ? previousData : undefined;
}

/** Console event types delivered by GET /api/v1/events/stream. */
export const CONSOLE_EVENT_TYPES = [
  'breaker.transition',
  'identity.state',
  'proxy.state',
  'alert',
  'config.published',
  'policy.published',
] as const;

export type ConsoleEventType = (typeof CONSOLE_EVENT_TYPES)[number];

/** Query domains invalidated when a console event arrives. */
export const EVENT_INVALIDATIONS: Record<ConsoleEventType, readonly QueryDomain[]> = {
  'breaker.transition': ['breakers', 'dashboard', 'sites'],
  'identity.state': ['identities', 'accounts', 'heatmap', 'dashboard'],
  'proxy.state': ['proxies', 'dashboard'],
  alert: ['notifications', 'dashboard'],
  'config.published': ['config'],
  'policy.published': ['policies', 'breakers', 'sites'],
};
