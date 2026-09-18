import { useNavigate } from '@tanstack/react-router';
import { Grid3X3Icon, MousePointerClickIcon, RefreshCwIcon } from 'lucide-react';
import { useCallback } from 'react';
import { useTranslation } from 'react-i18next';

import { useAuth } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';
import { EmptyState } from '@/components/EmptyState';
import { ErrorState } from '@/components/ErrorState';
import { PageHeader } from '@/components/PageHeader';
import { TimeAgo } from '@/components/TimeAgo';
import { Button } from '@/components/ui/button';
import { Card } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import { useSiteOptions } from '@/features/requests/shared/useSiteOptions';

import { type HeatmapSelection } from '../heatmapSearch';
import { HEATMAP_REFETCH_MS, useHeatmap } from '../useHeatmap';
import { HeatmapChart } from './HeatmapChart';
import { HeatmapControls } from './HeatmapControls';
import { HeatmapPager } from './HeatmapPager';
import { HeatmapTable } from './HeatmapTable';

export interface HeatmapViewProps {
  selection: HeatmapSelection;
  onSelectionChange: (selection: HeatmapSelection) => void;
}

/** Resolves the effective site and client: the URL choice, else the first available. */
function useEffectiveTarget(selection: HeatmapSelection) {
  const sites = useSiteOptions();
  const list = sites.data ?? [];
  const site = selection.site || list[0]?.name || '';
  const known = list.find((s) => s.name === site);
  const clients = known?.clients ?? (selection.client ? [selection.client] : []);
  const client =
    selection.client && clients.includes(selection.client) ? selection.client : (clients[0] ?? '');
  return { sites, site, client, clients };
}

/** Cooldown / score heatmap of a site client with chart and table views. */
export function HeatmapView({ selection, onSelectionChange }: HeatmapViewProps) {
  const { t } = useTranslation('heatmap');
  const { namespaceName, can } = useAuth();
  const navigate = useNavigate();
  const { sites, site, client, clients } = useEffectiveTarget(selection);
  const { pager, query, grid } = useHeatmap(site, client, selection);
  const canOpenIdentity = can(PERMISSIONS.identityRead, site);

  const openIdentity = useCallback(
    (id: string) => void navigate({ to: '/identities/$id', params: { id } }),
    [navigate],
  );

  if (!namespaceName) return <EmptyState title={t('noNamespace')} />;

  const header = (
    <PageHeader
      title={t('title')}
      description={t('description')}
      actions={
        <>
          {query.data?.generatedAt && (
            <span
              className="text-xs text-muted-foreground"
              title={t('autoRefresh', { seconds: HEATMAP_REFETCH_MS / 1000 })}
            >
              {t('common:time.updated', { time: '' })}
              <TimeAgo value={query.data.generatedAt} past />
            </span>
          )}
          <Button
            variant="outline"
            size="icon-sm"
            onClick={() => void query.refetch()}
            disabled={!site || !client}
            aria-label={t('common:actions.refresh')}
          >
            <RefreshCwIcon className={query.isFetching ? 'animate-spin' : undefined} />
          </Button>
        </>
      }
    />
  );

  if (sites.isLoading) {
    return (
      <>
        {header}
        <Skeleton className="h-[480px] w-full rounded-lg" />
      </>
    );
  }
  if (sites.isError && !sites.data) {
    return (
      <>
        {header}
        <ErrorState error={sites.error} onRetry={() => void sites.refetch()} />
      </>
    );
  }
  if (!site) {
    return (
      <>
        {header}
        <EmptyState icon={Grid3X3Icon} title={t('noSites')} description={t('noSitesDescription')} />
      </>
    );
  }

  const rows = grid?.rows.length ?? 0;
  return (
    <>
      {header}
      <div className="space-y-3">
        <HeatmapControls
          selection={selection}
          site={site}
          client={client}
          clients={clients}
          onChange={onSelectionChange}
        />
        {query.isLoading || (!grid && query.isFetching) ? (
          <Skeleton className="h-[480px] w-full rounded-lg" />
        ) : query.isError && !grid ? (
          <ErrorState error={query.error} onRetry={() => void query.refetch()} />
        ) : !grid || !client ? (
          <EmptyState icon={Grid3X3Icon} title={clients.length === 0 ? t('noClients') : t('noClient')} />
        ) : rows === 0 || grid.columns.length === 0 ? (
          <EmptyState
            icon={Grid3X3Icon}
            title={rows === 0 ? t('emptyTitle') : t('noGroups')}
            description={rows === 0 ? t('emptyDescription') : undefined}
          />
        ) : (
          <Card className="gap-3 p-3">
            {query.isError && (
              <ErrorState error={query.error} onRetry={() => void query.refetch()} compact className="py-2" />
            )}
            {selection.view === 'table' ? (
              <HeatmapTable grid={grid} metric={selection.metric} linkIdentities={canOpenIdentity} />
            ) : (
              <>
                <HeatmapChart
                  grid={grid}
                  metric={selection.metric}
                  onOpenIdentity={canOpenIdentity ? openIdentity : undefined}
                />
                {canOpenIdentity && (
                  <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
                    <MousePointerClickIcon className="size-3.5" aria-hidden />
                    {t('clickHint')}
                  </p>
                )}
              </>
            )}
            <HeatmapPager
              pager={pager}
              nextPageToken={query.data?.nextPageToken}
              rowCount={rows}
              limit={selection.limit}
              total={query.data?.total}
              disabled={query.isFetching && query.isPlaceholderData}
            />
          </Card>
        )}
      </div>
    </>
  );
}
