import { type QueryClient } from '@tanstack/react-query';

import { type GetMeResponse } from '@/gen/spinneret/v1/auth_pb';
import { setAuthBridge } from '@/lib/authBridge';
import { authClient } from '@/lib/clients';
import { isUnauthenticated } from '@/lib/errors';

import { resolveSelection } from './permissions';
import { selectionStore } from './selectionStore';

/** Query key of AuthService.GetMe; the cached value is null when signed out. */
export const ME_QUERY_KEY = ['auth', 'me'] as const;

/** Loads the current user; resolves to null (not an error) when there is no session. */
export async function fetchMe(): Promise<GetMeResponse | null> {
  try {
    return await authClient.getMe({});
  } catch (err) {
    if (isUnauthenticated(err)) return null;
    throw err;
  }
}

/** Marks the session as signed out and drops all cached console data. */
export function clearSession(queryClient: QueryClient): void {
  const isConsoleData = (query: { queryKey: readonly unknown[] }) => query.queryKey[0] !== 'auth';
  queryClient.setQueryData(ME_QUERY_KEY, null);
  void queryClient.cancelQueries({ predicate: isConsoleData });
  queryClient.removeQueries({ predicate: isConsoleData });
}

/**
 * Connects the transport to the session: the tenant header follows the
 * resolved active tenant and unauthenticated responses clear the session
 * (RequireAuth then redirects to /login?redirect=...).
 */
export function installAuthBridge(queryClient: QueryClient): () => void {
  return setAuthBridge({
    getTenantId: () => {
      const stored = selectionStore.get();
      const me = queryClient.getQueryData<GetMeResponse | null>(ME_QUERY_KEY);
      if (!me) return stored.tenantId;
      return resolveSelection(me.tenants, stored.tenantId, stored.namespace).tenant?.tenant?.id;
    },
    onUnauthenticated: () => clearSession(queryClient),
  });
}
