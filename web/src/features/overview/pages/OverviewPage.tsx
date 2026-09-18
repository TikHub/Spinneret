import {
  ActivityIcon,
  FingerprintIcon,
  GaugeIcon,
  RefreshCwIcon,
  SendIcon,
  ShieldAlertIcon,
  ShieldCheckIcon,
} from 'lucide-react';
import { useCallback, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';

import { useAuth } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';
import { RequirePermission } from '@/app/auth/PermissionGate';
import { OVERVIEW_REFETCH_MS } from '@/app/queryClient';
import { SEMANTIC_CHART_COLORS } from '@/components/charts/palette';
import { EmptyState } from '@/components/EmptyState';
import { ErrorState } from '@/components/ErrorState';
import { PageHeader } from '@/components/PageHeader';
import { PageIntro } from '@/components/PageIntro';
import { StatCard } from '@/components/StatCard';
import { TimeAgo } from '@/components/TimeAgo';
import { useTheme } from '@/app/theme/ThemeProvider';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { Skeleton } from '@/components/ui/skeleton';
import { formatNumber, formatPercent, formatRate } from '@/lib/format';

import { LowWatermarkCard } from '../components/LowWatermarkCard';
import { NodeStatsTable } from '../components/NodeStatsTable';
import { OpenBreakersCard } from '../components/OpenBreakersCard';
import { SiteCard } from '../components/SiteCard';
import { TrendChart } from '../components/TrendChart';
import { OVERVIEW_WINDOWS, useOverviewData, type OverviewWindow } from '../useOverviewData';

function OverviewContent() {
  const { t, i18n } = useTranslation();
  const lng = i18n.language;
  const { namespaceName } = useAuth();
  const { resolvedTheme } = useTheme();
  const [timeWindow, setTimeWindow] = useState<OverviewWindow>('5m');
  const data = useOverviewData(timeWindow);
  const { overview, acquireRate, successRatio, riskRatio, nodes, openBreakers, canReadBreakers } = data;

  const totals = overview.data?.totals;
  const sites = overview.data?.sites ?? [];
  const loading = overview.isPending;

  const refetchAll = () => {
    void overview.refetch();
    void acquireRate.refetch();
    void successRatio.refetch();
    void riskRatio.refetch();
    void nodes.refetch();
    if (canReadBreakers) void openBreakers.refetch();
  };

  const formatQps = useCallback((v: number) => formatRate(v, lng), [lng]);
  const formatRatio = useCallback((v: number) => formatPercent(v, 0, lng), [lng]);
  const acquireLines = useMemo(
    () => [
      {
        name: t('overview.trends.acquires'),
        series: acquireRate.data?.series,
        color: SEMANTIC_CHART_COLORS.primary[resolvedTheme],
      },
    ],
    [t, acquireRate.data, resolvedTheme],
  );
  const ratioLines = useMemo(
    () => [
      {
        name: t('overview.trends.success'),
        series: successRatio.data?.series,
        color: SEMANTIC_CHART_COLORS.success[resolvedTheme],
      },
      {
        name: t('overview.trends.risk'),
        series: riskRatio.data?.series,
        color: SEMANTIC_CHART_COLORS.risk[resolvedTheme],
      },
    ],
    [t, successRatio.data, riskRatio.data, resolvedTheme],
  );

  if (!namespaceName) {
    return <EmptyState title={t('overview.noNamespace')} />;
  }

  return (
    <>
      <PageHeader
        title={t('overview.title')}
        description={t('overview.description', { namespace: namespaceName })}
        actions={
          <>
            {overview.data?.generatedAt && (
              <span
                className="text-xs text-muted-foreground"
                title={t('overview.autoRefresh', { seconds: OVERVIEW_REFETCH_MS / 1000 })}
              >
                {t('time.updated', { time: '' })}
                <TimeAgo value={overview.data.generatedAt} past />
              </span>
            )}
            <Select value={timeWindow} onValueChange={(v) => setTimeWindow(v as OverviewWindow)}>
              <SelectTrigger size="sm" className="w-36" aria-label={t('overview.window')}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {OVERVIEW_WINDOWS.map((w) => (
                  <SelectItem key={w} value={w}>
                    {t('overview.windowOption', { window: w })}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Button variant="outline" size="icon-sm" onClick={refetchAll} aria-label={t('actions.refresh')}>
              <RefreshCwIcon className={overview.isFetching ? 'animate-spin' : undefined} />
            </Button>
          </>
        }
      />
      <PageIntro
        page="overview"
        links={[
          { to: '/sites', labelKey: 'nav.sites' },
          { to: '/heatmap', labelKey: 'nav.heatmap' },
        ]}
      />

      {overview.isError && !overview.data ? (
        <ErrorState error={overview.error} onRetry={() => void overview.refetch()} />
      ) : (
        <div className="space-y-5">
          <section className="grid grid-cols-2 gap-3 md:grid-cols-3 2xl:grid-cols-6">
            <StatCard
              label={t('overview.totals.availableIdentities')}
              icon={FingerprintIcon}
              loading={loading}
              value={formatNumber(totals?.availableIdentities, undefined, lng)}
            />
            <StatCard
              label={t('overview.totals.acquireQps')}
              icon={SendIcon}
              loading={loading}
              value={formatRate(totals?.acquireQps, lng)}
              hint={
                totals
                  ? t('overview.totals.acquireFailureRatio', {
                      ratio: formatPercent(totals.acquireFailureRatio, 1, lng),
                    })
                  : undefined
              }
            />
            <StatCard
              label={t('overview.totals.reportQps')}
              icon={ActivityIcon}
              loading={loading}
              value={formatRate(totals?.reportQps, lng)}
              hint={
                overview.data
                  ? `${t('overview.totals.streamsPending')} ${formatNumber(overview.data.streamsPending, undefined, lng)}`
                  : undefined
              }
            />
            <StatCard
              label={t('overview.totals.successRatio')}
              icon={ShieldCheckIcon}
              loading={loading}
              tone={
                !totals || totals.reportQps <= 0
                  ? 'default'
                  : totals.successRatio < 0.8
                    ? 'warning'
                    : 'success'
              }
              value={formatPercent(totals?.successRatio, 1, lng)}
              hint={
                totals
                  ? `${t('overview.totals.unknownRatio')} ${formatPercent(totals.unknownRatio, 1, lng)}`
                  : undefined
              }
            />
            <StatCard
              label={t('overview.totals.riskRatio')}
              icon={ShieldAlertIcon}
              loading={loading}
              tone={totals && totals.riskRatio >= 0.1 ? 'danger' : 'default'}
              value={formatPercent(totals?.riskRatio, 1, lng)}
            />
            <StatCard
              label={t('overview.totals.openBreakers')}
              icon={GaugeIcon}
              loading={loading}
              tone={totals && totals.openBreakers > 0 ? 'danger' : 'default'}
              value={formatNumber(totals?.openBreakers, undefined, lng)}
              hint={totals ? t('overview.totals.halfOpen', { count: totals.halfOpenBreakers }) : undefined}
            />
          </section>

          <section className="grid gap-3 lg:grid-cols-2">
            <Card>
              <CardHeader>
                <CardTitle>{t('overview.trends.acquireRate')}</CardTitle>
              </CardHeader>
              <CardContent>
                <TrendChart
                  lines={acquireLines}
                  formatValue={formatQps}
                  loading={acquireRate.isLoading}
                  error={acquireRate.error ?? undefined}
                  onRetry={() => void acquireRate.refetch()}
                  ariaLabel={t('overview.trends.acquireRate')}
                />
              </CardContent>
            </Card>
            <Card>
              <CardHeader>
                <CardTitle>{t('overview.trends.ratios')}</CardTitle>
              </CardHeader>
              <CardContent>
                <TrendChart
                  lines={ratioLines}
                  formatValue={formatRatio}
                  yMax={1}
                  loading={successRatio.isLoading || riskRatio.isLoading}
                  error={successRatio.error ?? riskRatio.error ?? undefined}
                  onRetry={() => {
                    void successRatio.refetch();
                    void riskRatio.refetch();
                  }}
                  ariaLabel={t('overview.trends.ratios')}
                />
              </CardContent>
            </Card>
          </section>

          <section className="space-y-2">
            <h2 className="text-sm font-semibold">{t('overview.sites')}</h2>
            {loading ? (
              <div className="grid gap-3 md:grid-cols-2 2xl:grid-cols-3">
                {Array.from({ length: 3 }, (_, i) => (
                  <Skeleton key={i} className="h-56 w-full rounded-lg" />
                ))}
              </div>
            ) : sites.length === 0 ? (
              <EmptyState title={t('overview.noSites')} description={t('overview.noSitesDescription')} />
            ) : (
              <div className="grid gap-3 md:grid-cols-2 2xl:grid-cols-3">
                {sites.map((site) => (
                  <SiteCard key={site.siteId || site.site} site={site} />
                ))}
              </div>
            )}
          </section>

          <section className="grid gap-3 lg:grid-cols-2">
            {canReadBreakers && (
              <OpenBreakersCard
                breakers={openBreakers.data?.breakers}
                total={openBreakers.data?.total}
                isLoading={openBreakers.isPending}
                error={openBreakers.error ?? undefined}
                onRetry={() => void openBreakers.refetch()}
              />
            )}
            <LowWatermarkCard sites={sites} />
          </section>

          <section className="space-y-2">
            <h2 className="text-sm font-semibold">{t('overview.nodes.title')}</h2>
            <NodeStatsTable
              nodes={nodes.data?.nodes}
              isLoading={nodes.isPending}
              error={nodes.error ?? undefined}
              onRetry={() => void nodes.refetch()}
            />
          </section>
        </div>
      )}
    </>
  );
}

/** Namespace overview: per-site health, trends, breakers, low watermarks and nodes (auto refresh 10 s). */
export default function OverviewPage() {
  return (
    <RequirePermission permission={PERMISSIONS.dashboardRead}>
      <OverviewContent />
    </RequirePermission>
  );
}
