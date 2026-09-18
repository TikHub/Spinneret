import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';

import { useAuth } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { DataTable, useCursorPagination, type DataTableColumn } from '@/components/data-table';
import { StateBadge } from '@/components/StateBadge';
import { TimeAgo } from '@/components/TimeAgo';
import { FormField } from '@/components/ui/form';
import { Switch } from '@/components/ui/switch';
import { Textarea } from '@/components/ui/textarea';
import { SimpleTooltip } from '@/components/ui/tooltip';
import { type Site } from '@/gen/spinneret/v1/site_admin_pb';
import { formatNumber } from '@/lib/format';

import { useSetSitePaused, useSites } from '../useBreakers';

export interface SiteSwitchesProps {
  /** Pauses auto refresh while a dialog elsewhere on the page is open. */
  paused: boolean;
}

/** Site switches: pause or resume all leases of a site (SetSitePaused). */
export function SiteSwitches({ paused }: SiteSwitchesProps) {
  const { t, i18n } = useTranslation('breakers');
  const { tenantId, namespaceName, can } = useAuth();
  const pager = useCursorPagination({ resetOn: [tenantId, namespaceName] });
  const [target, setTarget] = useState<Site | null>(null);
  const sites = useSites(pager, paused || target !== null);
  const setPaused = useSetSitePaused();
  const [reason, setReason] = useState('');
  const lng = i18n.language;

  const columns = useMemo<DataTableColumn<Site>[]>(
    () => [
      {
        id: 'site',
        header: t('sites.columns.site'),
        enableHiding: false,
        cell: ({ row }) => (
          <div className="grid gap-0.5">
            <span className="font-medium">{row.original.displayName || row.original.name}</span>
            {row.original.displayName && (
              <span className="font-mono text-xs text-muted-foreground">{row.original.name}</span>
            )}
          </div>
        ),
      },
      {
        id: 'clients',
        header: t('sites.columns.clients'),
        meta: { label: t('sites.columns.clients') },
        cell: ({ row }) => (
          <span className="font-mono text-xs">{row.original.clients.join(', ') || '—'}</span>
        ),
      },
      {
        id: 'groups',
        header: t('sites.columns.groups'),
        meta: { label: t('sites.columns.groups'), align: 'right' },
        cell: ({ row }) => (
          <span className="tabular">{formatNumber(row.original.endpointGroupCount, undefined, lng)}</span>
        ),
      },
      {
        id: 'state',
        header: t('sites.columns.state'),
        meta: { label: t('sites.columns.state') },
        cell: ({ row }) => <StateBadge kind="site" state={row.original.paused ? 'paused' : 'active'} />,
      },
      {
        id: 'reason',
        header: t('sites.columns.reason'),
        meta: { label: t('sites.columns.reason'), className: 'max-w-72' },
        cell: ({ row }) =>
          row.original.paused ? (
            <div className="grid gap-0.5 text-xs">
              <span className="line-clamp-2 break-words">
                {row.original.pausedReason || t('sites.noReason')}
              </span>
              <TimeAgo value={row.original.pausedAt} className="text-muted-foreground" past />
            </div>
          ) : (
            <span className="text-muted-foreground">—</span>
          ),
      },
      {
        id: 'switch',
        header: t('sites.columns.switch'),
        enableHiding: false,
        meta: { align: 'right' },
        cell: ({ row }) => {
          const site = row.original;
          const allowed = can(PERMISSIONS.breakerOperate, site.id);
          const control = (
            <Switch
              checked={!site.paused}
              disabled={!allowed}
              aria-label={
                site.paused
                  ? t('sites.resumeLabel', { site: site.name })
                  : t('sites.pauseLabel', { site: site.name })
              }
              onCheckedChange={() => {
                setReason('');
                setTarget(site);
              }}
            />
          );
          return (
            <div className="flex justify-end">
              {allowed ? (
                control
              ) : (
                <SimpleTooltip
                  content={t('common:permission.missing', { permission: PERMISSIONS.breakerOperate })}
                >
                  <span tabIndex={0} className="inline-flex">
                    {control}
                  </span>
                </SimpleTooltip>
              )}
            </div>
          );
        },
      },
    ],
    [t, lng, can],
  );

  const pausing = target !== null && !target.paused;

  return (
    <>
      <DataTable
        columns={columns}
        data={sites.data?.sites}
        getRowId={(s) => s.id}
        isLoading={sites.isLoading}
        isFetching={sites.isFetching}
        error={sites.error}
        onRetry={() => void sites.refetch()}
        emptyTitle={t('sites.empty')}
        emptyDescription={t('sites.emptyDescription')}
        rowClassName={(s) => (s.paused ? 'bg-amber-500/5' : undefined)}
        pagination={{ pager, nextPageToken: sites.data?.nextPageToken, total: sites.data?.total }}
      />
      <ConfirmDialog
        open={target !== null}
        onOpenChange={(open) => !open && setTarget(null)}
        destructive={pausing}
        title={
          pausing
            ? t('sites.pauseTitle', { site: target?.name ?? '' })
            : t('sites.resumeTitle', { site: target?.name ?? '' })
        }
        description={pausing ? t('sites.pauseDescription') : t('sites.resumeDescription')}
        confirmLabel={pausing ? t('sites.pause') : t('sites.resume')}
        confirmDisabled={pausing && reason.trim() === ''}
        onConfirm={() =>
          target
            ? setPaused.mutateAsync({ site: target.name, paused: pausing, reason: reason.trim() })
            : undefined
        }
      >
        <FormField
          label={t('reason')}
          required={pausing}
          description={pausing ? t('sites.pauseReasonHint') : t('reasonHint')}
        >
          <Textarea value={reason} onChange={(e) => setReason(e.target.value)} rows={2} maxLength={1024} />
        </FormField>
      </ConfirmDialog>
    </>
  );
}
