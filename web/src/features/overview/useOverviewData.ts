import { useQuery } from '@tanstack/react-query';

import { useAuth, useScopedPlaceholder, useScopedQueryKey } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';
import { OVERVIEW_REFETCH_MS } from '@/app/queryClient';
import { breakerClient, dashboardClient } from '@/lib/clients';
import { HOUR_MS, lastTimeRange } from '@/lib/time';

export const OVERVIEW_WINDOWS = ['1m', '5m', '15m', '1h'] as const;
export type OverviewWindow = (typeof OVERVIEW_WINDOWS)[number];

const OPEN_BREAKERS_LIMIT = 20;

/** Queries backing the overview page; all refresh every 10 s. */
export function useOverviewData(timeWindow: OverviewWindow) {
  const { namespaceName, can } = useAuth();
  const key = useScopedQueryKey();
  const keepPrevious = useScopedPlaceholder();
  const namespace = namespaceName ?? '';
  const enabled = namespace !== '' && can(PERMISSIONS.dashboardRead);
  const common = {
    enabled,
    refetchInterval: OVERVIEW_REFETCH_MS,
    placeholderData: keepPrevious,
  } as const;

  const overview = useQuery({
    ...common,
    queryKey: key('dashboard', 'overview', timeWindow),
    queryFn: ({ signal }) => dashboardClient.getOverview({ namespace, window: timeWindow }, { signal }),
  });

  const acquireRate = useQuery({
    ...common,
    queryKey: key('dashboard', 'timeseries', 'acquire_rate', '1h'),
    queryFn: ({ signal }) =>
      dashboardClient.getTimeSeries(
        { namespace, metric: 'acquire_rate', timeRange: lastTimeRange(HOUR_MS) },
        { signal },
      ),
  });

  const successRatio = useQuery({
    ...common,
    queryKey: key('dashboard', 'timeseries', 'success_ratio', '1h'),
    queryFn: ({ signal }) =>
      dashboardClient.getTimeSeries(
        { namespace, metric: 'success_ratio', timeRange: lastTimeRange(HOUR_MS) },
        { signal },
      ),
  });

  const riskRatio = useQuery({
    ...common,
    queryKey: key('dashboard', 'timeseries', 'risk_ratio', '1h'),
    queryFn: ({ signal }) =>
      dashboardClient.getTimeSeries(
        { namespace, metric: 'risk_ratio', timeRange: lastTimeRange(HOUR_MS) },
        { signal },
      ),
  });

  const nodes = useQuery({
    ...common,
    queryKey: key('dashboard', 'nodes', '1h'),
    queryFn: ({ signal }) =>
      dashboardClient.getNodeStats({ namespace, timeRange: lastTimeRange(HOUR_MS) }, { signal }),
  });

  const canReadBreakers = can(PERMISSIONS.breakerRead);
  const openBreakers = useQuery({
    ...common,
    enabled: namespace !== '' && canReadBreakers,
    queryKey: key('breakers', 'overview-open'),
    queryFn: ({ signal }) =>
      breakerClient.listBreakers(
        { namespace, states: ['open', 'half_open'], pageSize: OPEN_BREAKERS_LIMIT },
        { signal },
      ),
  });

  return { overview, acquireRate, successRatio, riskRatio, nodes, openBreakers, canReadBreakers };
}
