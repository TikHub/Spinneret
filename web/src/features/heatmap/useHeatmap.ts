import { useQuery } from '@tanstack/react-query';
import { useMemo } from 'react';

import { useAuth, useScopedPlaceholder, useScopedQueryKey } from '@/app/auth/AuthContext';
import { OVERVIEW_REFETCH_MS } from '@/app/queryClient';
import { useCursorPagination } from '@/components/data-table';
import { dashboardClient } from '@/lib/clients';

import { buildHeatmapGrid } from './heatmapModel';
import { type HeatmapSelection } from './heatmapSearch';

/** The heatmap refreshes like the overview (10 s). */
export const HEATMAP_REFETCH_MS = OVERVIEW_REFETCH_MS;

/**
 * Heatmap page of a site client. Both metrics are always filled by the
 * server, so switching the metric recolors the cached matrix without a fetch.
 */
export function useHeatmap(site: string, client: string, selection: HeatmapSelection) {
  const { tenantId, namespaceName } = useAuth();
  const namespace = namespaceName ?? '';
  const key = useScopedQueryKey();
  const keepPrevious = useScopedPlaceholder();
  const { states, limit, metric } = selection;
  const pager = useCursorPagination({
    pageSize: limit,
    resetOn: [tenantId, namespaceName, site, client, states, limit],
  });

  const query = useQuery({
    queryKey: key('heatmap', site, client, states, limit, pager.pageToken),
    queryFn: ({ signal }) =>
      dashboardClient.getHeatmap(
        { namespace, site, client, metric, states, limit, pageToken: pager.pageToken },
        { signal },
      ),
    enabled: namespace !== '' && site !== '' && client !== '',
    placeholderData: keepPrevious,
    refetchInterval: HEATMAP_REFETCH_MS,
  });

  // Structural sharing keeps unchanged parts referentially stable, so a refresh that only
  // moves generated_at does not rebuild the grid or redraw the chart.
  const data = query.data;
  const rows = data?.rows;
  const columns = data?.columns;
  const columnIds = data?.columnIds;
  const cells = data?.cells;
  const grid = useMemo(
    () =>
      rows && columns && columnIds && cells
        ? buildHeatmapGrid({ rows, columns, columnIds, cells })
        : undefined,
    [rows, columns, columnIds, cells],
  );
  return { pager, query, grid };
}
