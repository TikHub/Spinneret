import { create } from '@bufbuild/protobuf';
import { useQuery } from '@tanstack/react-query';

import { useAuth, useScopedPlaceholder, useScopedQueryKey } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';
import { LIVE_REFETCH_MS } from '@/app/queryClient';
import { type CursorPagination } from '@/components/data-table';
import { TimeRangeSchema } from '@/gen/spinneret/v1/common_pb';
import { notificationClient, siteClient } from '@/lib/clients';
import { toTimestamp } from '@/lib/time';

/** Which channels or alerts to list: every accessible one of the tenant, or the active namespace only. */
export type ListScope = 'all' | 'namespace';

/** Page of notification channels (live: refreshed every 5 s unless paused). */
export function useChannels(scope: ListScope, pager: CursorPagination, paused: boolean) {
  const { namespaceName, can } = useAuth();
  const key = useScopedQueryKey();
  const keepPrevious = useScopedPlaceholder();
  const namespace = scope === 'namespace' ? (namespaceName ?? '') : '';
  return useQuery({
    queryKey: key('notifications', 'channels', scope, pager.pageToken, pager.pageSize),
    queryFn: ({ signal }) =>
      notificationClient.listChannels(
        { namespace, pageSize: pager.pageSize, pageToken: pager.pageToken },
        { signal },
      ),
    enabled: can(PERMISSIONS.notifyRead) && (scope === 'all' || Boolean(namespaceName)),
    placeholderData: keepPrevious,
    refetchInterval: paused ? false : LIVE_REFETCH_MS,
  });
}

export interface AlertFilters {
  scope: ListScope;
  kind: string;
  severity: string;
  site: string;
  /** Lookback in ms; 0 = no bound. */
  rangeMs: number;
}

/** Page of fired alerts, newest first (live unless paused). */
export function useAlertEvents(filters: AlertFilters, pager: CursorPagination, paused: boolean) {
  const { namespaceName, can } = useAuth();
  const key = useScopedQueryKey();
  const keepPrevious = useScopedPlaceholder();
  const namespace = filters.scope === 'namespace' ? (namespaceName ?? '') : '';
  return useQuery({
    queryKey: key('notifications', 'alerts', filters, pager.pageToken, pager.pageSize),
    queryFn: ({ signal }) =>
      notificationClient.listAlertEvents(
        {
          namespace,
          site: namespace ? filters.site : '',
          kind: filters.kind,
          severity: filters.severity,
          // Lower bound only, so new alerts keep showing up while the page refreshes.
          ...(filters.rangeMs > 0
            ? { timeRange: create(TimeRangeSchema, { start: toTimestamp(Date.now() - filters.rangeMs) }) }
            : {}),
          pageSize: pager.pageSize,
          pageToken: pager.pageToken,
        },
        { signal },
      ),
    enabled: can(PERMISSIONS.notifyRead) && (filters.scope === 'all' || Boolean(namespaceName)),
    placeholderData: keepPrevious,
    refetchInterval: paused ? false : LIVE_REFETCH_MS,
  });
}

/** Site names of a namespace (sites picker and alert filter); disabled without site:read. */
export function useSiteNames(namespace: string, enabled: boolean) {
  const { can, namespaceName } = useAuth();
  const key = useScopedQueryKey();
  // Permissions are known for the active namespace only; other namespaces are attempted and may fail.
  const allowed = namespace !== namespaceName || can(PERMISSIONS.siteRead);
  return useQuery({
    queryKey: key('sites', 'names', namespace),
    queryFn: async ({ signal }) => {
      const res = await siteClient.listSites({ namespace, pageSize: 500 }, { signal });
      return res.sites.map((s) => s.name).sort((a, b) => a.localeCompare(b));
    },
    enabled: enabled && namespace !== '' && allowed,
    staleTime: 60_000,
  });
}
