import { Code } from '@connectrpc/connect';
import { useQuery, useQueryClient } from '@tanstack/react-query';

import { useAuth, useScopedPlaceholder, useScopedQueryKey } from '@/app/auth/AuthContext';
import { LIVE_REFETCH_MS } from '@/app/queryClient';
import { useCursorPagination } from '@/components/data-table';
import { dashboardClient } from '@/lib/clients';
import { hasCode, shouldRetryQuery, toApiError } from '@/lib/errors';

import { toQueryRequest, type RequestFilters } from './requestFilters';
import { useRangeAnchor } from './shared/useRangeAnchor';

/** Unused result pages of an explorer are dropped quickly (pages can hold 500 rows). */
const EXPLORER_GC_TIME_MS = 60_000;

/** Spinneret-Reason sent with the unavailable error of a server without ClickHouse. */
export const CLICKHOUSE_DISABLED_REASON = 'failed_precondition';

/**
 * QueryRequestEvents fails with unavailable (reason failed_precondition,
 * message naming ClickHouse) when ClickHouse is not configured. Other
 * unavailable errors (e.g. a 503 from a proxy) are ordinary, retryable errors.
 */
export function isClickHouseDisabled(error: unknown): boolean {
  if (!hasCode(error, Code.Unavailable)) return false;
  const { reason, message } = toApiError(error);
  return reason === CLICKHOUSE_DISABLED_REASON || /clickhouse/i.test(message);
}

function retryUnlessDisabled(failureCount: number, error: unknown): boolean {
  return !isClickHouseDisabled(error) && shouldRetryQuery(failureCount, error);
}

export interface UseRequestEventsOptions {
  /** Refresh the first page every LIVE_REFETCH_MS. */
  autoRefresh: boolean;
  /** Pauses auto refresh (e.g. while the detail sheet is open). */
  paused: boolean;
}

/**
 * Request explorer data: one page of events plus the summary, which is only
 * requested with the first page and kept visible while paging.
 */
export function useRequestEvents(filters: RequestFilters, { autoRefresh, paused }: UseRequestEventsOptions) {
  const { tenantId, namespaceName } = useAuth();
  const namespace = namespaceName ?? '';
  const queryClient = useQueryClient();
  const key = useScopedQueryKey();
  const keepPrevious = useScopedPlaceholder();
  const anchorFor = useRangeAnchor();
  const pager = useCursorPagination({ resetOn: [tenantId, namespaceName, filters] });

  const baseKey = key('requests', 'events', filters, pager.pageSize);
  const filterKey = JSON.stringify(baseKey);
  const fetchPage = (pageToken: string, signal: AbortSignal) =>
    dashboardClient.queryRequestEvents(
      {
        ...toQueryRequest(filters, namespace, anchorFor(filterKey, pageToken)),
        pageSize: pager.pageSize,
        pageToken,
        includeSummary: pageToken === '',
      },
      { signal },
    );

  const events = useQuery({
    queryKey: [...baseKey, pager.pageToken],
    queryFn: ({ signal }) => fetchPage(pager.pageToken, signal),
    enabled: namespace !== '',
    placeholderData: keepPrevious,
    gcTime: EXPLORER_GC_TIME_MS,
    retry: retryUnlessDisabled,
    refetchInterval: (query) =>
      autoRefresh && !paused && pager.pageIndex === 0 && !isClickHouseDisabled(query.state.error)
        ? LIVE_REFETCH_MS
        : false,
  });

  // Observes the first page (fetched by `events`) so its summary stays visible on later pages.
  const firstPage = useQuery({
    queryKey: [...baseKey, ''],
    queryFn: ({ signal }) => fetchPage('', signal),
    enabled: false,
    gcTime: EXPLORER_GC_TIME_MS,
  });

  // Refresh reloads the first page; from a later page the cached first page is
  // marked stale first so that returning to it fetches the newest events.
  const refresh = () => {
    if (pager.pageIndex === 0) {
      void events.refetch();
      return;
    }
    void queryClient.invalidateQueries({ queryKey: [...baseKey, ''], exact: true, refetchType: 'none' });
    pager.first();
  };

  const summary = firstPage.data?.summary;
  return {
    pager,
    events,
    refresh,
    summary,
    summaryLoading: summary === undefined && events.isFetching,
    clickHouseDisabled: isClickHouseDisabled(events.error),
  };
}
