import { ArrowRightIcon, BracesIcon, RefreshCwIcon } from 'lucide-react';
import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';

import { useAuth } from '@/app/auth/AuthContext';
import { DataTable, useCursorPagination, type DataTableColumn } from '@/components/data-table';
import { FilterBar } from '@/components/FilterBar';
import { JsonView } from '@/components/JsonView';
import { StateBadge } from '@/components/StateBadge';
import { TimeAgo } from '@/components/TimeAgo';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { type BreakerEvent } from '@/gen/spinneret/v1/breaker_admin_pb';
import { formatDateTime } from '@/lib/time';

import {
  BREAKER_TRIGGERS,
  HISTORY_RANGES,
  useBreakerEvents,
  useSiteEndpointGroups,
  useSites,
  type HistoryFilters,
  type HistoryRange,
} from '../useBreakers';

const ALL = '__all__';

export interface BreakerHistoryProps {
  initialSite: string;
}

/** Breaker transitions with site, endpoint group, trigger and time range filters. */
export function BreakerHistory({ initialSite }: BreakerHistoryProps) {
  const { t } = useTranslation('breakers');
  const { tenantId, namespaceName } = useAuth();
  const [filters, setFilters] = useState<HistoryFilters>({
    site: initialSite,
    endpointGroupId: '',
    trigger: '',
    range: '24h',
  });
  const pager = useCursorPagination({ resetOn: [tenantId, namespaceName, filters] });
  const events = useBreakerEvents(filters, pager);
  const sites = useSites();
  const groups = useSiteEndpointGroups(filters.site);

  const columns = useMemo<DataTableColumn<BreakerEvent>[]>(
    () => [
      {
        id: 'time',
        header: t('history.columns.time'),
        enableHiding: false,
        cell: ({ row }) => <TimeAgo value={row.original.createdAt} past />,
      },
      {
        id: 'target',
        header: t('history.columns.target'),
        meta: { label: t('history.columns.target') },
        cell: ({ row }) => {
          const e = row.original;
          return (
            <div className="grid gap-0.5">
              <span className="font-medium">{e.endpointGroup || t('history.siteSwitch')}</span>
              <span className="text-xs text-muted-foreground">
                {[e.site, e.client].filter(Boolean).join(' · ')}
              </span>
            </div>
          );
        },
      },
      {
        id: 'transition',
        header: t('history.columns.transition'),
        meta: { label: t('history.columns.transition') },
        cell: ({ row }) => (
          <span className="inline-flex items-center gap-1.5">
            <StateBadge kind="breaker" state={row.original.fromState} />
            <ArrowRightIcon className="size-3.5 text-muted-foreground" aria-label={t('history.to')} />
            <StateBadge kind="breaker" state={row.original.toState} />
          </span>
        ),
      },
      {
        id: 'trigger',
        header: t('history.columns.trigger'),
        meta: { label: t('history.columns.trigger') },
        cell: ({ row }) => (
          <Badge variant={row.original.trigger === 'manual' ? 'secondary' : 'outline'}>
            {t(`triggers.${row.original.trigger}`, { defaultValue: row.original.trigger })}
          </Badge>
        ),
      },
      {
        id: 'reason',
        header: t('history.columns.reason'),
        meta: { label: t('history.columns.reason'), className: 'max-w-80' },
        cell: ({ row }) =>
          row.original.reason ? (
            <span className="line-clamp-2 text-xs break-words" title={row.original.reason}>
              {row.original.reason}
            </span>
          ) : (
            <span className="text-muted-foreground">—</span>
          ),
      },
      {
        id: 'openUntil',
        header: t('history.columns.openUntil'),
        meta: { label: t('history.columns.openUntil') },
        cell: ({ row }) => {
          const e = row.original;
          if (e.openUntil) return <span className="text-xs tabular">{formatDateTime(e.openUntil)}</span>;
          if (e.toState === 'open' && e.endpointGroupId)
            return <span className="text-xs">{t('indefinite')}</span>;
          return <span className="text-muted-foreground">—</span>;
        },
      },
      {
        id: 'actor',
        header: t('history.columns.actor'),
        meta: { label: t('history.columns.actor') },
        cell: ({ row }) => <span className="font-mono text-xs">{row.original.actor || '—'}</span>,
      },
      {
        id: 'metrics',
        header: () => <span className="sr-only">{t('history.columns.metrics')}</span>,
        enableHiding: false,
        meta: { align: 'right' },
        cell: ({ row }) =>
          row.original.metrics && Object.keys(row.original.metrics).length > 0 ? (
            <Popover>
              <PopoverTrigger asChild>
                <Button variant="ghost" size="sm" aria-label={t('history.showMetrics')}>
                  <BracesIcon />
                  {t('history.metrics')}
                </Button>
              </PopoverTrigger>
              <PopoverContent align="end" className="w-96 max-w-[90vw]">
                <JsonView value={row.original.metrics} collapseDepth={3} />
              </PopoverContent>
            </Popover>
          ) : null,
      },
    ],
    [t],
  );

  const activeCount =
    (filters.site ? 1 : 0) +
    (filters.endpointGroupId ? 1 : 0) +
    (filters.trigger ? 1 : 0) +
    (filters.range !== '24h' ? 1 : 0);
  const siteNames = sites.data?.sites.map((s) => s.name) ?? [];
  if (filters.site && !siteNames.includes(filters.site)) siteNames.push(filters.site);

  const toolbar = (
    <FilterBar
      activeCount={activeCount}
      onReset={() => setFilters({ site: '', endpointGroupId: '', trigger: '', range: '24h' })}
      actions={
        <Button
          variant="outline"
          size="icon-sm"
          onClick={() => void events.refetch()}
          aria-label={t('common:actions.refresh')}
        >
          <RefreshCwIcon className={events.isFetching ? 'animate-spin' : undefined} />
        </Button>
      }
    >
      <Select
        value={filters.site || ALL}
        onValueChange={(v) => setFilters((f) => ({ ...f, site: v === ALL ? '' : v, endpointGroupId: '' }))}
      >
        <SelectTrigger size="sm" className="w-44" aria-label={t('filters.site')}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={ALL}>{t('filters.allSites')}</SelectItem>
          {siteNames.map((name) => (
            <SelectItem key={name} value={name}>
              {name}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <Select
        value={filters.endpointGroupId || ALL}
        onValueChange={(v) => setFilters((f) => ({ ...f, endpointGroupId: v === ALL ? '' : v }))}
        disabled={!filters.site}
      >
        <SelectTrigger size="sm" className="w-52" aria-label={t('filters.group')}>
          <SelectValue placeholder={t('filters.allGroups')} />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={ALL}>{t('filters.allGroups')}</SelectItem>
          {groups.data?.endpointGroups.map((g) => (
            <SelectItem key={g.id} value={g.id}>
              {g.client} / {g.name}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <Select
        value={filters.trigger || ALL}
        onValueChange={(v) => setFilters((f) => ({ ...f, trigger: v === ALL ? '' : v }))}
      >
        <SelectTrigger size="sm" className="w-40" aria-label={t('filters.trigger')}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={ALL}>{t('filters.allTriggers')}</SelectItem>
          {BREAKER_TRIGGERS.map((tr) => (
            <SelectItem key={tr} value={tr}>
              {t(`triggers.${tr}`)}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <Select
        value={filters.range}
        onValueChange={(v) => setFilters((f) => ({ ...f, range: v as HistoryRange }))}
      >
        <SelectTrigger size="sm" className="w-36" aria-label={t('filters.range')}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {HISTORY_RANGES.map((r) => (
            <SelectItem key={r} value={r}>
              {t(`ranges.${r}`)}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </FilterBar>
  );

  return (
    <DataTable
      columns={columns}
      data={events.data?.events}
      getRowId={(e) => e.id}
      isLoading={events.isLoading}
      isFetching={events.isFetching}
      error={events.error}
      onRetry={() => void events.refetch()}
      emptyTitle={t('history.empty')}
      emptyDescription={t('history.emptyDescription')}
      toolbar={toolbar}
      pagination={{ pager, nextPageToken: events.data?.nextPageToken }}
    />
  );
}
