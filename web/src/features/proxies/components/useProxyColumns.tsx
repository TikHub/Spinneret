import { LoaderCircleIcon, StethoscopeIcon } from 'lucide-react';
import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';

import { useAuth } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';
import { CopyButton, IdText } from '@/components/CopyButton';
import { type DataTableColumn } from '@/components/data-table';
import { Button } from '@/components/ui/button';
import { SimpleTooltip } from '@/components/ui/tooltip';
import { type Proxy } from '@/gen/spinneret/v1/proxy_admin_pb';
import { formatNumber } from '@/lib/format';
import { cn } from '@/lib/utils';

import { ProxyActionsMenu, type ProxyAction } from './ProxyActionsMenu';
import { LastCheckCell, ProxyStateCell, TagList } from './ProxyCells';

const dash = <span className="text-muted-foreground">—</span>;

export interface ProxyColumnOptions {
  onAction: (action: ProxyAction, proxy: Proxy) => void;
  /** Proxies with a health check in progress. */
  checkingIds: ReadonlySet<string>;
}

/** Column definitions of the proxies table. */
export function useProxyColumns({ onAction, checkingIds }: ProxyColumnOptions): DataTableColumn<Proxy>[] {
  const { t, i18n } = useTranslation('proxies');
  const lng = i18n.language;
  const canOperate = useAuth().can(PERMISSIONS.proxyOperate);

  return useMemo<DataTableColumn<Proxy>[]>(
    () => [
      {
        id: 'id',
        accessorKey: 'id',
        header: t('columns.id'),
        meta: { label: t('columns.id') },
        cell: ({ row }) => <IdText value={row.original.id} truncate={14} />,
      },
      {
        id: 'url',
        accessorKey: 'displayUrl',
        header: t('columns.url'),
        enableHiding: false,
        meta: { label: t('columns.url') },
        cell: ({ row }) => (
          <span className="inline-flex max-w-72 items-center gap-0.5">
            <span className="truncate font-mono text-xs" title={row.original.displayUrl}>
              {row.original.displayUrl}
            </span>
            <CopyButton value={row.original.displayUrl} />
          </span>
        ),
      },
      {
        id: 'username',
        accessorKey: 'usernameHint',
        header: t('columns.username'),
        meta: { label: t('columns.username') },
        cell: ({ row }) =>
          row.original.usernameHint ? (
            <span className="font-mono text-xs">{row.original.usernameHint}</span>
          ) : (
            dash
          ),
      },
      {
        id: 'kind',
        accessorKey: 'kind',
        header: t('columns.kind'),
        meta: { label: t('columns.kind') },
        cell: ({ row }) => t(`kinds.${row.original.kind}`, { defaultValue: row.original.kind }),
      },
      {
        id: 'location',
        accessorFn: (p) => `${p.region}/${p.city}`,
        header: t('columns.location'),
        meta: { label: t('columns.location') },
        cell: ({ row }) =>
          row.original.region || row.original.city
            ? [row.original.region, row.original.city].filter(Boolean).join(' / ')
            : dash,
      },
      {
        id: 'provider',
        accessorKey: 'provider',
        header: t('columns.provider'),
        meta: { label: t('columns.provider') },
        cell: ({ row }) => row.original.provider || dash,
      },
      {
        id: 'maxConcurrency',
        accessorKey: 'maxConcurrency',
        header: t('columns.maxConcurrency'),
        meta: { label: t('columns.maxConcurrency'), align: 'right', className: 'tabular' },
        cell: ({ row }) => formatNumber(row.original.maxConcurrency, undefined, lng),
      },
      {
        id: 'tags',
        accessorFn: (p) => p.tags.join(','),
        header: t('columns.tags'),
        enableSorting: false,
        meta: { label: t('columns.tags') },
        cell: ({ row }) => <TagList tags={row.original.tags} />,
      },
      {
        id: 'state',
        accessorKey: 'state',
        header: t('columns.state'),
        meta: { label: t('columns.state') },
        cell: ({ row }) => <ProxyStateCell proxy={row.original} />,
      },
      {
        id: 'lastCheck',
        accessorFn: (p) => Number(p.lastCheckAt?.seconds ?? 0n),
        header: t('columns.lastCheck'),
        meta: { label: t('columns.lastCheck') },
        cell: ({ row }) => <LastCheckCell proxy={row.original} />,
      },
      {
        id: 'failures',
        accessorKey: 'consecutiveCheckFailures',
        header: t('columns.failures'),
        meta: { label: t('columns.failures'), align: 'right', className: 'tabular' },
        cell: ({ row }) => (
          <span
            className={cn(row.original.consecutiveCheckFailures > 0 && 'text-rose-600 dark:text-rose-400')}
          >
            {formatNumber(row.original.consecutiveCheckFailures, undefined, lng)}
          </span>
        ),
      },
      {
        id: 'identities',
        accessorKey: 'boundIdentities',
        header: t('columns.identities'),
        meta: { label: t('columns.identities'), align: 'right', className: 'tabular' },
        cell: ({ row }) => formatNumber(row.original.boundIdentities, undefined, lng),
      },
      {
        id: 'exitIp',
        accessorKey: 'exitIp',
        header: t('columns.exitIp'),
        meta: { label: t('columns.exitIp') },
        cell: ({ row }) =>
          row.original.exitIp ? <span className="font-mono text-xs">{row.original.exitIp}</span> : dash,
      },
      {
        id: 'actions',
        header: () => <span className="sr-only">{t('columns.actions')}</span>,
        enableSorting: false,
        enableHiding: false,
        meta: { align: 'right' },
        cell: ({ row }) => {
          const proxy = row.original;
          const checking = checkingIds.has(proxy.id);
          return (
            <span className="inline-flex items-center gap-0.5">
              <SimpleTooltip
                content={
                  canOperate
                    ? t('check.now')
                    : t('common:permission.missing', { permission: PERMISSIONS.proxyOperate })
                }
              >
                <span className="inline-flex" tabIndex={canOperate ? undefined : 0}>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    className="size-7"
                    disabled={!canOperate || checking}
                    onClick={() => onAction('check', proxy)}
                    aria-label={t('check.nowFor', { name: proxy.displayUrl })}
                  >
                    {checking ? <LoaderCircleIcon className="animate-spin" /> : <StethoscopeIcon />}
                  </Button>
                </span>
              </SimpleTooltip>
              <ProxyActionsMenu proxy={proxy} onAction={onAction} />
            </span>
          );
        },
      },
    ],
    [t, lng, canOperate, checkingIds, onAction],
  );
}
