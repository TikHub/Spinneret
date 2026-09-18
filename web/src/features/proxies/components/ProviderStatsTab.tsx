import { useNavigate, useSearch } from '@tanstack/react-router';
import { BarChart3Icon, RefreshCwIcon } from 'lucide-react';
import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';

import { useTheme } from '@/app/theme/ThemeProvider';
import { EChart } from '@/components/charts/EChart';
import { SEMANTIC_CHART_COLORS } from '@/components/charts/palette';
import { DataTable, type DataTableColumn } from '@/components/data-table';
import { EmptyState } from '@/components/EmptyState';
import { ErrorState } from '@/components/ErrorState';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { SimpleTooltip } from '@/components/ui/tooltip';
import { type ProviderStats } from '@/gen/spinneret/v1/proxy_admin_pb';
import { formatLatency, formatNumber, formatPercent, toNumber } from '@/lib/format';
import { cn } from '@/lib/utils';

import { EMPTY_PROXY_FILTERS, proxyFiltersToSearch } from '../proxyFilters';
import {
  buildProviderChartOption,
  CHART_PROVIDER_LIMIT,
  parseProviderRange,
  PROVIDER_RANGES,
  toProviderChartData,
  type ProviderRange,
} from '../providerStats';
import { useProviderStats } from '../useProxies';

const CHART_HEIGHT = 280;

function useProviderColumns(onShowProxies: (provider: string) => void): DataTableColumn<ProviderStats>[] {
  const { t, i18n } = useTranslation('proxies');
  const lng = i18n.language;
  return useMemo<DataTableColumn<ProviderStats>[]>(
    () => [
      {
        id: 'provider',
        accessorKey: 'provider',
        header: t('providers.provider'),
        cell: ({ row }) =>
          row.original.provider ? (
            <SimpleTooltip content={t('providers.showProxies', { provider: row.original.provider })}>
              <Button
                variant="link"
                size="sm"
                className="h-auto p-0"
                onClick={() => onShowProxies(row.original.provider)}
              >
                {row.original.provider}
              </Button>
            </SimpleTooltip>
          ) : (
            <span className="text-muted-foreground italic">{t('providers.none')}</span>
          ),
      },
      ...(
        [
          ['proxies', (s: ProviderStats) => s.proxies],
          ['active', (s: ProviderStats) => s.active],
          ['dead', (s: ProviderStats) => s.dead],
          ['requests', (s: ProviderStats) => toNumber(s.requests)],
        ] as const
      ).map(([id, get]): DataTableColumn<ProviderStats> => ({
        id,
        accessorFn: get,
        header: t(`providers.${id}`),
        meta: { align: 'right', className: 'tabular' },
        cell: ({ row }) => (
          <span className={cn(id === 'dead' && get(row.original) > 0 && 'text-rose-600 dark:text-rose-400')}>
            {formatNumber(get(row.original), undefined, lng)}
          </span>
        ),
      })),
      {
        id: 'success',
        accessorKey: 'successRatio',
        header: t('providers.success'),
        meta: { align: 'right', className: 'tabular' },
        cell: ({ row }) =>
          toNumber(row.original.requests) > 0 ? formatPercent(row.original.successRatio, 1, lng) : '—',
      },
      {
        id: 'risk',
        accessorKey: 'riskRatio',
        header: t('providers.risk'),
        meta: { align: 'right', className: 'tabular' },
        cell: ({ row }) =>
          toNumber(row.original.requests) > 0 ? formatPercent(row.original.riskRatio, 1, lng) : '—',
      },
      {
        id: 'latency',
        accessorKey: 'avgLatencyMs',
        header: t('providers.latency'),
        meta: { align: 'right', className: 'tabular' },
        cell: ({ row }) =>
          toNumber(row.original.requests) > 0 ? formatLatency(row.original.avgLatencyMs) : '—',
      },
    ],
    [t, lng, onShowProxies],
  );
}

/** Provider statistics: request outcome ratios chart and pool health table. */
export function ProviderStatsTab() {
  const { t, i18n } = useTranslation('proxies');
  const lng = i18n.language;
  const { resolvedTheme } = useTheme();
  const search = useSearch({ from: '/_app/proxies' });
  const navigate = useNavigate({ from: '/proxies' });
  const range = parseProviderRange(search.range);
  const query = useProviderStats(range);
  const providers = query.data?.providers;

  const setRange = (next: ProviderRange) =>
    void navigate({ search: (prev) => ({ ...prev, range: next }), replace: true });
  // Shows only this provider's proxies: other filters are cleared so they cannot hide them, and
  // the list is serialized like the filter bar does (a provider name may contain a comma).
  const showProxies = useMemo(
    () => (provider: string) =>
      void navigate({
        search: (prev) => ({
          ...prev,
          tab: undefined,
          ...proxyFiltersToSearch({ ...EMPTY_PROXY_FILTERS, providers: [provider] }),
        }),
      }),
    [navigate],
  );
  const columns = useProviderColumns(showProxies);

  const chartData = useMemo(() => toProviderChartData(providers ?? [], t('providers.none')), [providers, t]);
  const option = useMemo(
    () =>
      buildProviderChartOption(
        chartData,
        { success: t('providers.success'), risk: t('providers.risk'), requests: t('providers.requests') },
        {
          successColor: SEMANTIC_CHART_COLORS.primary[resolvedTheme],
          riskColor: SEMANTIC_CHART_COLORS.risk[resolvedTheme],
          formatPercent: (v) => formatPercent(v, 0, lng),
          formatCount: (v) => formatNumber(v, undefined, lng),
        },
      ),
    [chartData, t, resolvedTheme, lng],
  );

  return (
    <div className="grid gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <Select value={range} onValueChange={(v) => setRange(v as ProviderRange)}>
          <SelectTrigger size="sm" className="w-40" aria-label={t('providers.range')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {PROVIDER_RANGES.map((r) => (
              <SelectItem key={r} value={r}>
                {t(`providers.ranges.${r}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Button
          variant="outline"
          size="icon-sm"
          className="ml-auto"
          onClick={() => void query.refetch()}
          aria-label={t('common:actions.refresh')}
        >
          <RefreshCwIcon className={cn(query.isFetching && 'animate-spin')} />
        </Button>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>{t('providers.chartTitle')}</CardTitle>
          <CardDescription>
            {t('providers.chartDescription', { count: CHART_PROVIDER_LIMIT })}
          </CardDescription>
        </CardHeader>
        <CardContent>
          {query.isError && !providers ? (
            <ErrorState error={query.error} onRetry={() => void query.refetch()} compact />
          ) : !query.isLoading && chartData.categories.length === 0 ? (
            <EmptyState compact icon={BarChart3Icon} title={t('providers.chartEmpty')} />
          ) : (
            <EChart
              option={option}
              height={CHART_HEIGHT}
              loading={query.isLoading}
              aria-label={t('providers.chartTitle')}
            />
          )}
        </CardContent>
      </Card>

      <DataTable
        columns={columns}
        data={providers}
        getRowId={(p) => p.provider || '__none__'}
        isLoading={query.isLoading}
        isFetching={query.isFetching}
        error={query.error}
        onRetry={() => void query.refetch()}
        enableColumnVisibility={false}
        emptyTitle={t('providers.empty')}
        emptyDescription={t('providers.emptyDescription')}
      />
    </div>
  );
}
