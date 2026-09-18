import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useCallback } from 'react';

import { useAuth, useScopedPlaceholder, useScopedQueryKey } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';
import { LIVE_REFETCH_MS } from '@/app/queryClient';
import { type CursorPagination } from '@/components/data-table';
import { proxyClient, siteClient } from '@/lib/clients';
import { sensitiveMutation } from '@/lib/sensitiveMutation';
import { lastTimeRange } from '@/lib/time';

import { type ImportFormat, type ProxyDefaultsInit } from './importProxies';
import { type ProxyFilters } from './proxyFilters';
import { type UpdateProxyInit } from './proxyForm';
import { type OperateProxiesInit } from './proxyOperations';
import { PROVIDER_RANGE_MS, type ProviderRange } from './providerStats';

/** Page size used to load option lists (sites for site-scoped operations). */
const OPTIONS_PAGE_SIZE = 500;

/** Invalidates every proxies query (and the dashboard, which shows pool health). */
export function useInvalidateProxies() {
  const queryClient = useQueryClient();
  return useCallback(() => {
    void queryClient.invalidateQueries({ queryKey: ['proxies'] });
    void queryClient.invalidateQueries({ queryKey: ['dashboard'] });
  }, [queryClient]);
}

/** Paged proxy list; refreshes every 5 s unless `paused`. */
export function useProxyList(filters: ProxyFilters, pager: CursorPagination, paused: boolean) {
  const { namespaceName } = useAuth();
  const key = useScopedQueryKey();
  const keepPrevious = useScopedPlaceholder();
  return useQuery({
    queryKey: key('proxies', 'list', filters, pager.pageToken, pager.pageSize),
    queryFn: ({ signal }) =>
      proxyClient.listProxies(
        {
          namespace: namespaceName ?? '',
          states: filters.states,
          kinds: filters.kinds,
          providers: filters.providers,
          regions: filters.regions,
          tags: filters.tags,
          search: filters.search.trim(),
          pageSize: pager.pageSize,
          pageToken: pager.pageToken,
        },
        { signal },
      ),
    enabled: Boolean(namespaceName),
    placeholderData: keepPrevious,
    refetchInterval: paused ? false : LIVE_REFETCH_MS,
  });
}

/** One proxy with its per-site hot state; refreshes every 5 s while shown. */
export function useProxyDetail(id: string | undefined, paused: boolean) {
  const key = useScopedQueryKey();
  return useQuery({
    queryKey: key('proxies', 'detail', id),
    queryFn: ({ signal }) => proxyClient.getProxy({ id: id ?? '' }, { signal }),
    enabled: Boolean(id),
    refetchInterval: paused ? false : LIVE_REFETCH_MS,
  });
}

/** Provider statistics for the selected time range. */
export function useProviderStats(range: ProviderRange, enabled = true) {
  const { namespaceName } = useAuth();
  const key = useScopedQueryKey();
  const keepPrevious = useScopedPlaceholder();
  return useQuery({
    queryKey: key('proxies', 'provider-stats', range),
    queryFn: ({ signal }) =>
      proxyClient.getProviderStats(
        { namespace: namespaceName ?? '', timeRange: lastTimeRange(PROVIDER_RANGE_MS[range]) },
        { signal },
      ),
    enabled: enabled && Boolean(namespaceName),
    placeholderData: keepPrevious,
  });
}

/** Site names for site-scoped operations (empty when the user cannot read sites). */
export function useSiteOptions(enabled: boolean) {
  const { namespaceName, can } = useAuth();
  const key = useScopedQueryKey();
  return useQuery({
    queryKey: key('sites', 'options'),
    queryFn: ({ signal }) =>
      siteClient.listSites({ namespace: namespaceName ?? '', pageSize: OPTIONS_PAGE_SIZE }, { signal }),
    enabled: enabled && Boolean(namespaceName) && can(PERMISSIONS.siteRead),
    select: (res) => res.sites.map((site) => ({ name: site.name, label: site.displayName || site.name })),
  });
}

export function useOperateProxies() {
  const invalidate = useInvalidateProxies();
  return useMutation({
    mutationFn: (req: OperateProxiesInit) => proxyClient.operateProxies(req),
    onSettled: invalidate,
  });
}

export function useDeleteProxies() {
  const invalidate = useInvalidateProxies();
  return useMutation({
    mutationFn: (ids: string[]) => proxyClient.deleteProxies({ ids }),
    onSettled: invalidate,
  });
}

export function useCheckProxy() {
  const invalidate = useInvalidateProxies();
  return useMutation({
    mutationFn: (id: string) => proxyClient.checkProxy({ id }),
    onSettled: invalidate,
  });
}

/** Updates proxy attributes; a replaced URL can embed credentials, so the mutation is sensitive. */
export function useUpdateProxy() {
  const invalidate = useInvalidateProxies();
  return useMutation(
    sensitiveMutation({
      mutationFn: (req: UpdateProxyInit) => proxyClient.updateProxy(req),
      onSuccess: invalidate,
    }),
  );
}

export interface ImportProxiesInput {
  format: ImportFormat;
  data: string;
  defaults: ProxyDefaultsInit;
  dryRun: boolean;
}

/** Imports proxies; the data holds proxy URLs with credentials, so the mutation is sensitive. */
export function useImportProxies() {
  const invalidate = useInvalidateProxies();
  const { namespaceName } = useAuth();
  return useMutation(
    sensitiveMutation({
      mutationFn: (input: ImportProxiesInput) =>
        proxyClient.importProxies({ namespace: namespaceName ?? '', ...input }),
      onSuccess: (_res, input) => {
        if (!input.dryRun) invalidate();
      },
    }),
  );
}
