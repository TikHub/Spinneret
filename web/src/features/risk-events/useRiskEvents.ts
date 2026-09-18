import { useQuery, useQueryClient } from '@tanstack/react-query';

import { useAuth, useScopedPlaceholder, useScopedQueryKey } from '@/app/auth/AuthContext';
import { LIVE_REFETCH_MS } from '@/app/queryClient';
import { useCursorPagination } from '@/components/data-table';
import { useRangeAnchor } from '@/features/requests/shared/useRangeAnchor';
import { dashboardClient } from '@/lib/clients';

import { toRiskEventsRequest, type RiskFilters } from './riskFilters';

/** One page of risk events for the filters (cursor pagination, optional auto refresh of page one). */
export function useRiskEvents(filters: RiskFilters, autoRefresh: boolean) {
  const { tenantId, namespaceName } = useAuth();
  const namespace = namespaceName ?? '';
  const queryClient = useQueryClient();
  const key = useScopedQueryKey();
  const keepPrevious = useScopedPlaceholder();
  const anchorFor = useRangeAnchor();
  const pager = useCursorPagination({ resetOn: [tenantId, namespaceName, filters] });

  const baseKey = key('risk-events', 'list', filters, pager.pageSize);
  const filterKey = JSON.stringify(baseKey);

  const events = useQuery({
    queryKey: [...baseKey, pager.pageToken],
    queryFn: ({ signal }) =>
      dashboardClient.listRiskEvents(
        {
          ...toRiskEventsRequest(filters, namespace, anchorFor(filterKey, pager.pageToken)),
          pageSize: pager.pageSize,
          pageToken: pager.pageToken,
        },
        { signal },
      ),
    enabled: namespace !== '',
    placeholderData: keepPrevious,
    refetchInterval: autoRefresh && pager.pageIndex === 0 ? LIVE_REFETCH_MS : false,
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

  return { pager, events, refresh };
}
