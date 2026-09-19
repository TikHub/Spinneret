import { create } from '@bufbuild/protobuf';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useSyncExternalStore,
  type ReactNode,
} from 'react';

import {
  GetMeResponseSchema,
  type NamespaceAccess,
  type TenantAccess,
  type User,
} from '@/gen/spinneret/v1/auth_pb';
import { authClient } from '@/lib/clients';
import { shouldRetryQuery } from '@/lib/errors';
import { keepPreviousInScope, scopedKey, type QueryDomain } from '@/lib/queryKeys';

import { checkPermission, checkTenantPermission, resolveSelection } from './permissions';
import { selectionStore } from './selectionStore';
import { clearSession, fetchMe, ME_QUERY_KEY } from './session';

export type AuthStatus = 'loading' | 'authenticated' | 'anonymous' | 'error';

export interface AuthContextValue {
  status: AuthStatus;
  /** GetMe error when status is "error". */
  error: unknown;
  user: User | undefined;
  isPlatformAdmin: boolean;
  /** Build version of the server, empty until the first GetMe answers. */
  serverVersion: string;
  /** Accessible tenants with effective permissions. */
  tenants: readonly TenantAccess[];
  /** Active tenant access. */
  tenant: TenantAccess | undefined;
  tenantId: string | undefined;
  /** Namespaces of the active tenant. */
  namespaces: readonly NamespaceAccess[];
  /** Active namespace access. */
  namespace: NamespaceAccess | undefined;
  /** Active namespace name (pass it as `namespace` in namespace-scoped requests). */
  namespaceName: string | undefined;
  setTenant: (tenantId: string) => void;
  setNamespace: (namespaceName: string) => void;
  /** Permission check in the active namespace; `site` is a site ID or name. */
  can: (permission: string, site?: string) => boolean;
  /** True when any of the permissions is granted. */
  canAny: (permissions: readonly string[], site?: string) => boolean;
  /**
   * Permission check for tenant-level resources (users, role bindings,
   * tenant-wide notification channels): needs a binding covering the whole tenant.
   */
  canInTenant: (permission: string) => boolean;
  login: (username: string, password: string) => Promise<void>;
  logout: () => Promise<void>;
  /** Reloads GetMe (e.g. after role changes). */
  refresh: () => Promise<void>;
}

const AuthContext = createContext<AuthContextValue | undefined>(undefined);

const ME_STALE_TIME_MS = 60_000;

export function AuthProvider({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient();
  const meQuery = useQuery({
    queryKey: ME_QUERY_KEY,
    queryFn: fetchMe,
    staleTime: ME_STALE_TIME_MS,
    refetchOnWindowFocus: true,
    retry: shouldRetryQuery,
  });
  const stored = useSyncExternalStore(selectionStore.subscribe, selectionStore.get, selectionStore.get);

  const me = meQuery.data ?? undefined;
  const tenants = useMemo(() => me?.tenants ?? [], [me]);
  const selection = useMemo(
    () => resolveSelection(tenants, stored.tenantId, stored.namespace),
    [tenants, stored.tenantId, stored.namespace],
  );
  const tenantId = selection.tenant?.tenant?.id;
  const namespaceName = selection.namespace?.namespace?.name;
  const isPlatformAdmin = me?.user?.isPlatformAdmin ?? false;

  // Persist the effective selection (e.g. when the stored tenant is no longer accessible).
  useEffect(() => {
    if (me) selectionStore.sync(tenantId, namespaceName);
  }, [me, tenantId, namespaceName]);

  let status: AuthStatus;
  if (meQuery.isPending) status = 'loading';
  else if (meQuery.isError && meQuery.data === undefined) status = 'error';
  else status = me ? 'authenticated' : 'anonymous';

  const setTenant = useCallback(
    (nextTenantId: string) => {
      const next = resolveSelection(tenants, nextTenantId, selectionStore.get().namespace);
      const id = next.tenant?.tenant?.id;
      if (id) selectionStore.setTenant(id, next.namespace?.namespace?.name);
    },
    [tenants],
  );

  const setNamespace = useCallback((name: string) => selectionStore.setNamespace(name), []);

  const can = useCallback(
    (permission: string, site?: string) =>
      checkPermission(
        { isPlatformAdmin, namespace: selection.namespace, bindings: selection.tenant?.bindings },
        permission,
        site,
      ),
    [isPlatformAdmin, selection.namespace, selection.tenant],
  );

  const canAny = useCallback(
    (permissions: readonly string[], site?: string) => permissions.some((p) => can(p, site)),
    [can],
  );

  const canInTenant = useCallback(
    (permission: string) => checkTenantPermission({ isPlatformAdmin, tenant: selection.tenant }, permission),
    [isPlatformAdmin, selection.tenant],
  );

  const login = useCallback(
    async (username: string, password: string) => {
      const res = await authClient.login({ username, password });
      clearSession(queryClient);
      queryClient.setQueryData(
        ME_QUERY_KEY,
        create(GetMeResponseSchema, {
          user: res.user,
          tenants: res.tenants,
          serverVersion: res.serverVersion,
        }),
      );
    },
    [queryClient],
  );

  const logout = useCallback(async () => {
    try {
      await authClient.logout({});
    } finally {
      clearSession(queryClient);
    }
  }, [queryClient]);

  const refetchMe = meQuery.refetch;
  const refresh = useCallback(async () => {
    await refetchMe();
  }, [refetchMe]);

  const value = useMemo<AuthContextValue>(
    () => ({
      status,
      error: meQuery.error,
      user: me?.user,
      isPlatformAdmin,
      serverVersion: me?.serverVersion ?? '',
      tenants,
      tenant: selection.tenant,
      tenantId,
      namespaces: selection.tenant?.namespaces ?? [],
      namespace: selection.namespace,
      namespaceName,
      setTenant,
      setNamespace,
      can,
      canAny,
      canInTenant,
      login,
      logout,
      refresh,
    }),
    [
      status,
      meQuery.error,
      me,
      isPlatformAdmin,
      tenants,
      selection,
      tenantId,
      namespaceName,
      setTenant,
      setNamespace,
      can,
      canAny,
      canInTenant,
      login,
      logout,
      refresh,
    ],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

/** Returns the auth context; must be used inside AuthProvider. */
// eslint-disable-next-line react-refresh/only-export-components
export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error('useAuth must be used inside AuthProvider');
  return ctx;
}

/** Shorthand for useAuth().can(permission, site). */
// eslint-disable-next-line react-refresh/only-export-components
export function usePermission(permission: string, site?: string): boolean {
  return useAuth().can(permission, site);
}

/**
 * Returns a builder for query keys scoped to the active tenant and namespace:
 * key('identities', filters, pageToken) -> ['identities', tenantId, namespace, filters, pageToken].
 */
// eslint-disable-next-line react-refresh/only-export-components
export function useScopedQueryKey(): (domain: QueryDomain, ...parts: unknown[]) => readonly unknown[] {
  const { tenantId, namespaceName } = useAuth();
  return useCallback(
    (domain: QueryDomain, ...parts: unknown[]) => scopedKey(domain, tenantId, namespaceName, ...parts),
    [tenantId, namespaceName],
  );
}

/**
 * Returns a `placeholderData` function for scoped queries: the previous data
 * stays visible while the next page or filter loads, but not across a tenant or
 * namespace switch. Use it instead of keepPreviousData:
 * `useQuery({ queryKey: key('proxies', filters), placeholderData: keepPrevious, ... })`.
 */
// eslint-disable-next-line react-refresh/only-export-components
export function useScopedPlaceholder(): ReturnType<typeof keepPreviousInScope> {
  const { tenantId, namespaceName } = useAuth();
  return useMemo(() => keepPreviousInScope(tenantId, namespaceName), [tenantId, namespaceName]);
}
