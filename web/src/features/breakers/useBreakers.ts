import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import { useAuth, useScopedPlaceholder, useScopedQueryKey } from '@/app/auth/AuthContext';
import { LIVE_REFETCH_MS } from '@/app/queryClient';
import { type CursorPagination } from '@/components/data-table';
import { breakerClient, siteClient } from '@/lib/clients';
import { lastTimeRange } from '@/lib/time';

export const BREAKER_STATES = ['open', 'half_open', 'closed'] as const;
export const BREAKER_TRIGGERS = ['auto', 'manual', 'probe', 'site_switch'] as const;
export const HISTORY_RANGES = ['1h', '6h', '24h', '7d', '30d', 'all'] as const;
export type HistoryRange = (typeof HISTORY_RANGES)[number];

const RANGE_MS: Record<Exclude<HistoryRange, 'all'>, number> = {
  '1h': 3_600_000,
  '6h': 6 * 3_600_000,
  '24h': 24 * 3_600_000,
  '7d': 7 * 86_400_000,
  '30d': 30 * 86_400_000,
};

const OPTIONS_PAGE_SIZE = 500;

export interface BreakerFilters {
  site: string;
  client: string;
  states: string[];
}

/** Live breaker list: refreshes every 5 s unless paused (dialog open). */
export function useBreakerList(filters: BreakerFilters, pager: CursorPagination, paused: boolean) {
  const { namespaceName } = useAuth();
  const key = useScopedQueryKey();
  const keepPrevious = useScopedPlaceholder();
  return useQuery({
    queryKey: key('breakers', 'list', filters, pager.pageToken, pager.pageSize),
    queryFn: ({ signal }) =>
      breakerClient.listBreakers(
        { namespace: namespaceName ?? '', ...filters, pageSize: pager.pageSize, pageToken: pager.pageToken },
        { signal },
      ),
    enabled: Boolean(namespaceName),
    placeholderData: keepPrevious,
    refetchInterval: paused ? false : LIVE_REFETCH_MS,
  });
}

/** Sites of the namespace (filters and site switches). */
export function useSites(pager?: CursorPagination, paused = false) {
  const { namespaceName } = useAuth();
  const key = useScopedQueryKey();
  const keepPrevious = useScopedPlaceholder();
  const pageSize = pager?.pageSize ?? OPTIONS_PAGE_SIZE;
  const pageToken = pager?.pageToken ?? '';
  return useQuery({
    queryKey: key('sites', 'breaker-sites', pageToken, pageSize),
    queryFn: ({ signal }) =>
      siteClient.listSites({ namespace: namespaceName ?? '', pageSize, pageToken }, { signal }),
    enabled: Boolean(namespaceName),
    placeholderData: keepPrevious,
    refetchInterval: pager && !paused ? LIVE_REFETCH_MS : false,
  });
}

/** Endpoint groups of a site (history filter). */
export function useSiteEndpointGroups(site: string) {
  const { namespaceName } = useAuth();
  const key = useScopedQueryKey();
  return useQuery({
    queryKey: key('sites', 'breaker-endpoint-groups', site),
    queryFn: ({ signal }) =>
      siteClient.listEndpointGroups(
        { namespace: namespaceName ?? '', site, pageSize: OPTIONS_PAGE_SIZE },
        { signal },
      ),
    enabled: Boolean(namespaceName) && site !== '',
    staleTime: 30_000,
  });
}

export interface HistoryFilters {
  site: string;
  endpointGroupId: string;
  trigger: string;
  range: HistoryRange;
}

/** Breaker transitions, newest first. The time range is computed when the page is fetched. */
export function useBreakerEvents(filters: HistoryFilters, pager: CursorPagination) {
  const { namespaceName } = useAuth();
  const key = useScopedQueryKey();
  const keepPrevious = useScopedPlaceholder();
  return useQuery({
    queryKey: key('breakers', 'events', filters, pager.pageToken, pager.pageSize),
    queryFn: ({ signal }) =>
      breakerClient.listBreakerEvents(
        {
          namespace: namespaceName ?? '',
          site: filters.site,
          endpointGroupId: filters.endpointGroupId,
          trigger: filters.trigger,
          timeRange: filters.range === 'all' ? undefined : lastTimeRange(RANGE_MS[filters.range]),
          pageSize: pager.pageSize,
          pageToken: pager.pageToken,
        },
        { signal },
      ),
    enabled: Boolean(namespaceName),
    placeholderData: keepPrevious,
  });
}

function useInvalidateBreakers() {
  const queryClient = useQueryClient();
  return () =>
    Promise.all([
      queryClient.invalidateQueries({ queryKey: ['breakers'] }),
      queryClient.invalidateQueries({ queryKey: ['dashboard'] }),
      queryClient.invalidateQueries({ queryKey: ['sites'] }),
    ]);
}

export function useOpenBreaker() {
  const { t } = useTranslation('breakers');
  const invalidate = useInvalidateBreakers();
  return useMutation({
    mutationFn: (input: { endpointGroupId: string; duration: string; reason: string }) =>
      breakerClient.openBreaker(input),
    onSuccess: (res) => {
      toast.success(t('toasts.opened', { group: res.breaker?.endpointGroup ?? '' }));
      void invalidate();
    },
  });
}

export function useCloseBreaker() {
  const { t } = useTranslation('breakers');
  const invalidate = useInvalidateBreakers();
  return useMutation({
    mutationFn: (input: { endpointGroupId: string; reason: string }) => breakerClient.closeBreaker(input),
    onSuccess: (res) => {
      toast.success(t('toasts.closed', { group: res.breaker?.endpointGroup ?? '' }));
      void invalidate();
    },
  });
}

export function useSetSitePaused() {
  const { t } = useTranslation('breakers');
  const { namespaceName } = useAuth();
  const invalidate = useInvalidateBreakers();
  return useMutation({
    mutationFn: (input: { site: string; paused: boolean; reason: string }) =>
      breakerClient.setSitePaused({ namespace: namespaceName ?? '', ...input }),
    onSuccess: (res) => {
      toast.success(
        res.paused ? t('toasts.paused', { site: res.site }) : t('toasts.resumed', { site: res.site }),
      );
      void invalidate();
    },
  });
}
