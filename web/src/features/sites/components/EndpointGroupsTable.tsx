import { EllipsisIcon, ListOrderedIcon, PlusIcon, TriangleAlertIcon } from 'lucide-react';
import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';

import { useAuth } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';
import { PermissionButton } from '@/app/auth/PermissionGate';
import { IdText } from '@/components/CopyButton';
import { DataTable, useCursorPagination, type DataTableColumn } from '@/components/data-table';
import { StateBadge } from '@/components/StateBadge';
import { TimeAgo } from '@/components/TimeAgo';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import { SimpleTooltip } from '@/components/ui/tooltip';
import { type EndpointGroup, type Site } from '@/gen/spinneret/v1/site_admin_pb';
import { formatNumber } from '@/lib/format';
import { cn } from '@/lib/utils';

import { useSitePermissions } from '../useSitePermissions';
import { useEndpointGroups } from '../useSites';
import { DEFAULT_GROUP_NAME } from '../uriRules';

export type GroupAction = 'rules' | 'edit' | 'delete';

export interface EndpointGroupsTableProps {
  site: Site;
  client: string;
  /** Pause auto refresh while a dialog is open. */
  paused: boolean;
  onAction: (action: GroupAction, group: EndpointGroup) => void;
  onCreate: () => void;
}

function GroupActionsMenu({
  group,
  canWrite,
  onAction,
}: {
  group: EndpointGroup;
  canWrite: boolean;
  onAction: EndpointGroupsTableProps['onAction'];
}) {
  const { t } = useTranslation('sites');
  const isDefault = group.name === DEFAULT_GROUP_NAME;
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon-sm"
          className="size-7"
          aria-label={t('groups.actionsFor', { name: group.name })}
        >
          <EllipsisIcon />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        {!canWrite && (
          <DropdownMenuLabel className="max-w-56 font-normal">
            {t('common:permission.missing', { permission: PERMISSIONS.siteWrite })}
          </DropdownMenuLabel>
        )}
        <DropdownMenuItem onSelect={() => onAction('rules', group)}>{t('groups.editRules')}</DropdownMenuItem>
        <DropdownMenuItem disabled={!canWrite} onSelect={() => onAction('edit', group)}>
          {t('common:actions.edit')}
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        <DropdownMenuItem
          variant="destructive"
          disabled={!canWrite || isDefault}
          onSelect={() => onAction('delete', group)}
        >
          {isDefault ? t('groups.defaultNotDeletable') : t('common:actions.delete')}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

function useGroupColumns(site: Site, onAction: EndpointGroupsTableProps['onAction']) {
  const { t, i18n } = useTranslation('sites');
  const lng = i18n.language;
  const { canWriteSite } = useSitePermissions();
  const canWrite = canWriteSite(site.id);

  return useMemo<DataTableColumn<EndpointGroup>[]>(
    () => [
      {
        id: 'name',
        accessorKey: 'name',
        header: t('groups.columns.name'),
        enableHiding: false,
        meta: { label: t('groups.columns.name') },
        cell: ({ row }) => (
          <span className="grid gap-0.5">
            <span className="flex items-center gap-1.5">
              <span className="font-mono text-sm font-medium">{row.original.name}</span>
              {row.original.name === DEFAULT_GROUP_NAME && (
                <Badge variant="outline">{t('groups.fallback')}</Badge>
              )}
            </span>
            <IdText value={row.original.id} truncate={12} className="text-muted-foreground" />
          </span>
        ),
      },
      {
        id: 'description',
        accessorKey: 'description',
        header: t('groups.columns.description'),
        meta: { label: t('groups.columns.description'), className: 'max-w-72 truncate' },
        cell: ({ row }) =>
          row.original.description ? (
            <span title={row.original.description}>{row.original.description}</span>
          ) : (
            <span className="text-muted-foreground">—</span>
          ),
      },
      {
        id: 'lowWatermark',
        accessorKey: 'lowWatermark',
        header: t('groups.columns.lowWatermark'),
        meta: { label: t('groups.columns.lowWatermark'), align: 'right', className: 'tabular' },
        cell: ({ row }) =>
          row.original.lowWatermark > 0 ? (
            formatNumber(row.original.lowWatermark, undefined, lng)
          ) : (
            <span className="text-muted-foreground">{t('groups.alertOff')}</span>
          ),
      },
      {
        id: 'available',
        accessorKey: 'availableIdentities',
        header: t('groups.columns.available'),
        meta: { label: t('groups.columns.available'), align: 'right', className: 'tabular' },
        cell: ({ row }) => {
          const low =
            row.original.lowWatermark > 0 && row.original.availableIdentities < row.original.lowWatermark;
          return (
            <span
              className={cn(
                'inline-flex items-center gap-1',
                low && 'font-medium text-amber-700 dark:text-amber-400',
              )}
            >
              {low && (
                <SimpleTooltip content={t('groups.belowWatermark')}>
                  <TriangleAlertIcon className="size-3.5" aria-label={t('groups.belowWatermark')} />
                </SimpleTooltip>
              )}
              {formatNumber(row.original.availableIdentities, undefined, lng)}
            </span>
          );
        },
      },
      {
        id: 'breaker',
        accessorKey: 'breakerState',
        header: t('groups.columns.breaker'),
        meta: { label: t('groups.columns.breaker') },
        cell: ({ row }) => <StateBadge kind="breaker" state={row.original.breakerState || 'closed'} />,
      },
      {
        id: 'rules',
        accessorFn: (g) => g.rules.length,
        header: t('groups.columns.rules'),
        meta: { label: t('groups.columns.rules'), align: 'right' },
        cell: ({ row }) =>
          row.original.name === DEFAULT_GROUP_NAME ? (
            <span className="text-muted-foreground">—</span>
          ) : (
            <Button
              variant="ghost"
              size="sm"
              className="h-7 tabular"
              onClick={() => onAction('rules', row.original)}
            >
              <ListOrderedIcon />
              {formatNumber(row.original.rules.length, undefined, lng)}
            </Button>
          ),
      },
      {
        id: 'updated',
        accessorFn: (g) => Number(g.updatedAt?.seconds ?? 0n),
        header: t('groups.columns.updated'),
        meta: { label: t('groups.columns.updated') },
        cell: ({ row }) => (
          <TimeAgo value={row.original.updatedAt} past className="text-xs text-muted-foreground" />
        ),
      },
      {
        id: 'actions',
        header: () => <span className="sr-only">{t('groups.columns.actions')}</span>,
        enableSorting: false,
        enableHiding: false,
        meta: { align: 'right' },
        cell: ({ row }) => <GroupActionsMenu group={row.original} canWrite={canWrite} onAction={onAction} />,
      },
    ],
    [t, lng, canWrite, onAction],
  );
}

/** Endpoint groups of one site client with live availability and breaker state. */
export function EndpointGroupsTable({ site, client, paused, onAction, onCreate }: EndpointGroupsTableProps) {
  const { t } = useTranslation('sites');
  const { tenantId, namespaceName } = useAuth();
  const pager = useCursorPagination({ resetOn: [tenantId, namespaceName, site.name, client] });
  const query = useEndpointGroups(site.name, client, pager, paused);
  const columns = useGroupColumns(site, onAction);

  return (
    <DataTable
      columns={columns}
      data={query.data?.endpointGroups}
      getRowId={(g) => g.id}
      isLoading={query.isLoading}
      isFetching={query.isFetching}
      error={query.error}
      onRetry={() => void query.refetch()}
      onRowClick={(group) => onAction('rules', group)}
      emptyTitle={t('groups.empty')}
      emptyDescription={t('groups.emptyDescription')}
      initialColumnVisibility={{ updated: false }}
      pagination={{ pager, nextPageToken: query.data?.nextPageToken, total: query.data?.total }}
      toolbar={
        <PermissionButton permission={PERMISSIONS.siteWrite} site={site.id} size="sm" onClick={onCreate}>
          <PlusIcon />
          {t('groups.create')}
        </PermissionButton>
      }
    />
  );
}
