import { Link } from '@tanstack/react-router';
import { DoorClosedIcon, DoorOpenIcon, HandIcon, PauseIcon } from 'lucide-react';
import { useMemo, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';

import { PERMISSIONS } from '@/app/auth/permissions';
import { PermissionButton } from '@/app/auth/PermissionGate';
import { IdText } from '@/components/CopyButton';
import { DataTable, type DataTableColumn, type DataTablePagination } from '@/components/data-table';
import { StateBadge } from '@/components/StateBadge';
import { TimeAgo } from '@/components/TimeAgo';
import { Badge } from '@/components/ui/badge';
import { type BreakerStatus } from '@/gen/spinneret/v1/breaker_admin_pb';
import { formatNumber } from '@/lib/format';

import { OpenUntil, RatioBar } from './MetricBits';

export interface BreakerTableProps {
  breakers: readonly BreakerStatus[] | undefined;
  isLoading: boolean;
  isFetching: boolean;
  error: unknown;
  onRetry: () => void;
  pagination: DataTablePagination;
  onOpen: (breaker: BreakerStatus) => void;
  onClose: (breaker: BreakerStatus) => void;
  toolbar?: ReactNode;
  emptyDescription?: string;
}

/** Breaker state per endpoint group with window and probe metrics and manual open/close. */
export function BreakerTable({
  breakers,
  isLoading,
  isFetching,
  error,
  onRetry,
  pagination,
  onOpen,
  onClose,
  toolbar,
  emptyDescription,
}: BreakerTableProps) {
  const { t, i18n } = useTranslation('breakers');
  const lng = i18n.language;

  const columns = useMemo<DataTableColumn<BreakerStatus>[]>(
    () => [
      {
        id: 'group',
        header: t('columns.group'),
        enableHiding: false,
        cell: ({ row }) => {
          const b = row.original;
          return (
            <div className="grid min-w-44 gap-0.5">
              <span className="font-medium">{b.endpointGroup}</span>
              <span className="text-xs text-muted-foreground">
                {b.site} · {b.client}
              </span>
              <IdText value={b.endpointGroupId} truncate={14} className="text-muted-foreground" />
            </div>
          );
        },
      },
      {
        id: 'state',
        header: t('columns.state'),
        meta: { label: t('columns.state') },
        cell: ({ row }) => {
          const b = row.original;
          return (
            <div className="flex flex-col items-start gap-1">
              <StateBadge kind="breaker" state={b.state} />
              <div className="flex flex-wrap gap-1">
                {b.manual && (
                  <Badge variant="outline" className="gap-1">
                    <HandIcon />
                    {t('manual')}
                  </Badge>
                )}
                {b.sitePaused && (
                  <Badge
                    variant="outline"
                    className="gap-1 border-amber-500/40 text-amber-700 dark:text-amber-400"
                  >
                    <PauseIcon />
                    {t('sitePaused')}
                  </Badge>
                )}
              </div>
            </div>
          );
        },
      },
      {
        id: 'openUntil',
        header: t('columns.openUntil'),
        meta: { label: t('columns.openUntil') },
        cell: ({ row }) => <OpenUntil state={row.original.state} openUntil={row.original.openUntil} />,
      },
      {
        id: 'consecutiveOpens',
        header: t('columns.consecutiveOpens'),
        meta: { label: t('columns.consecutiveOpens'), align: 'right' },
        cell: ({ row }) => (
          <span className="tabular">{formatNumber(row.original.consecutiveOpens, undefined, lng)}</span>
        ),
      },
      {
        id: 'window',
        header: t('columns.window'),
        meta: { label: t('columns.window') },
        cell: ({ row }) => {
          const w = row.original.window;
          if (!w || w.total === 0)
            return <span className="text-xs text-muted-foreground">{t('noTraffic')}</span>;
          return (
            <div className="grid gap-1">
              <span className="text-xs tabular">
                {t('windowSummary', {
                  total: formatNumber(w.total, undefined, lng),
                  success: formatNumber(w.success, undefined, lng),
                  risk: formatNumber(w.risk, undefined, lng),
                })}
              </span>
              <span className="text-xs text-muted-foreground tabular">
                {t('captchaIdentities', { count: w.captchaIdentities })}
              </span>
              <RatioBar label={t('success')} ratio={w.successRatio} tone="success" />
              <RatioBar label={t('risk')} ratio={w.riskRatio} tone="risk" />
            </div>
          );
        },
      },
      {
        id: 'probe',
        header: t('columns.probe'),
        meta: { label: t('columns.probe') },
        cell: ({ row }) => {
          const b = row.original;
          const p = b.probe;
          if (b.state !== 'half_open' || !p) return <span className="text-muted-foreground">—</span>;
          return (
            <span className="text-xs tabular">
              {t('probeSummary', { successes: p.successes, samples: p.samples, issued: p.issued })}
            </span>
          );
        },
      },
      {
        id: 'reason',
        header: t('columns.reason'),
        meta: { label: t('columns.reason'), className: 'max-w-64' },
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
        id: 'lastTransition',
        header: t('columns.lastTransition'),
        meta: { label: t('columns.lastTransition') },
        cell: ({ row }) => {
          const b = row.original;
          return (
            <div className="grid gap-0.5 text-xs">
              <span>
                <span className="text-muted-foreground">{t('lastOpened')} </span>
                <TimeAgo value={b.lastOpenedAt} fallback={t('never')} past />
              </span>
              <span>
                <span className="text-muted-foreground">{t('lastClosed')} </span>
                <TimeAgo value={b.lastClosedAt} fallback={t('never')} past />
              </span>
            </div>
          );
        },
      },
      {
        id: 'policy',
        header: t('columns.policy'),
        meta: { label: t('columns.policy') },
        cell: ({ row }) =>
          row.original.policyId ? (
            <Link
              to="/policies"
              search={{ id: row.original.policyId }}
              className="font-mono text-xs text-primary underline-offset-4 hover:underline"
            >
              {row.original.policyName || row.original.policyId}
            </Link>
          ) : (
            <span className="text-xs text-muted-foreground">{t('builtinPolicy')}</span>
          ),
      },
      {
        id: 'actions',
        header: () => <span className="sr-only">{t('columns.actions')}</span>,
        enableHiding: false,
        meta: { align: 'right' },
        cell: ({ row }) => {
          const b = row.original;
          return (
            <div className="flex justify-end gap-1">
              {b.state !== 'open' && (
                <PermissionButton
                  permission={PERMISSIONS.breakerOperate}
                  site={b.siteId || b.site}
                  variant="outline"
                  size="sm"
                  className="text-destructive hover:text-destructive"
                  onClick={() => onOpen(b)}
                >
                  <DoorOpenIcon />
                  {t('actions.open')}
                </PermissionButton>
              )}
              {b.state !== 'closed' && (
                <PermissionButton
                  permission={PERMISSIONS.breakerOperate}
                  site={b.siteId || b.site}
                  variant="outline"
                  size="sm"
                  onClick={() => onClose(b)}
                >
                  <DoorClosedIcon />
                  {t('actions.close')}
                </PermissionButton>
              )}
            </div>
          );
        },
      },
    ],
    [t, lng, onOpen, onClose],
  );

  return (
    <DataTable
      columns={columns}
      data={breakers}
      getRowId={(b) => b.endpointGroupId}
      isLoading={isLoading}
      isFetching={isFetching}
      error={error}
      onRetry={onRetry}
      emptyTitle={t('empty')}
      emptyDescription={emptyDescription}
      pagination={pagination}
      toolbar={toolbar}
      initialColumnVisibility={{ lastTransition: false }}
      rowClassName={(b) =>
        b.state === 'open' ? 'bg-rose-500/5' : b.state === 'half_open' ? 'bg-amber-500/5' : undefined
      }
    />
  );
}
