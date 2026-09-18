import { CircleCheckIcon, CircleXIcon, FlaskConicalIcon, PencilIcon, Trash2Icon } from 'lucide-react';
import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';

import { PERMISSIONS } from '@/app/auth/permissions';
import { IdText } from '@/components/CopyButton';
import { type DataTableColumn } from '@/components/data-table';
import { TimeAgo } from '@/components/TimeAgo';
import { Badge } from '@/components/ui/badge';
import { Switch } from '@/components/ui/switch';
import { SimpleTooltip } from '@/components/ui/tooltip';
import { type Channel } from '@/gen/spinneret/v1/notification_admin_pb';

import { useChannelAccess } from '../useChannelAccess';
import { ChannelKindLabel, SeverityBadge } from './badges';
import { RowButton } from './RowButton';

const VISIBLE_EVENT_TYPES = 2;

export interface ChannelRowActions {
  onToggle: (channel: Channel, enabled: boolean) => void;
  onTest: (channel: Channel) => void;
  onEdit: (channel: Channel) => void;
  onDelete: (channel: Channel) => void;
  /** Channel IDs with a pending test delivery. */
  testing: ReadonlySet<string>;
}

/** Columns of the channels table. */
export function useChannelColumns({ onToggle, onTest, onEdit, onDelete, testing }: ChannelRowActions) {
  const { t } = useTranslation('notifications');
  const access = useChannelAccess();
  return useMemo<DataTableColumn<Channel>[]>(
    () => [
      {
        id: 'name',
        header: t('fields.name'),
        enableHiding: false,
        meta: { label: t('fields.name') },
        cell: ({ row }) => (
          <span className="grid">
            <span className="font-medium">{row.original.name}</span>
            <IdText value={row.original.id} className="text-muted-foreground" />
          </span>
        ),
      },
      {
        id: 'kind',
        header: t('fields.kind'),
        meta: { label: t('fields.kind') },
        cell: ({ row }) => <ChannelKindLabel kind={row.original.kind} />,
      },
      {
        id: 'scope',
        header: t('fields.scope'),
        meta: { label: t('fields.scope') },
        cell: ({ row }) =>
          row.original.namespace === '' ? (
            <Badge variant="secondary">{t('scope.tenant')}</Badge>
          ) : (
            <Badge variant="outline" className="font-mono">
              {row.original.namespace}
            </Badge>
          ),
      },
      {
        id: 'eventTypes',
        header: t('fields.eventTypes'),
        meta: { label: t('fields.eventTypes') },
        cell: ({ row }) => {
          const kinds = row.original.eventTypes;
          const labels = kinds.map((k) => t(`alertKinds.${k}.label`, { defaultValue: k }));
          return (
            <SimpleTooltip content={labels.join(', ')}>
              <span className="flex flex-wrap gap-1">
                {labels.slice(0, VISIBLE_EVENT_TYPES).map((label, i) => (
                  <Badge key={kinds[i]} variant="outline">
                    {label}
                  </Badge>
                ))}
                {kinds.length > VISIBLE_EVENT_TYPES && (
                  <Badge variant="muted">+{kinds.length - VISIBLE_EVENT_TYPES}</Badge>
                )}
              </span>
            </SimpleTooltip>
          );
        },
      },
      {
        id: 'sites',
        header: t('fields.sites'),
        meta: { label: t('fields.sites'), className: 'max-w-48' },
        cell: ({ row }) =>
          row.original.sites.length === 0 ? (
            <span className="text-muted-foreground">{t('picker.sitesAll')}</span>
          ) : (
            <span className="block truncate font-mono text-xs" title={row.original.sites.join(', ')}>
              {row.original.sites.join(', ')}
            </span>
          ),
      },
      {
        id: 'minSeverity',
        header: t('fields.minSeverity'),
        meta: { label: t('fields.minSeverity') },
        cell: ({ row }) => <SeverityBadge severity={row.original.minSeverity || 'warning'} />,
      },
      {
        id: 'lastDelivery',
        header: t('fields.lastDelivery'),
        meta: { label: t('fields.lastDelivery') },
        cell: ({ row }) => {
          const { lastDeliveryAt, lastDeliveryStatus } = row.original;
          if (!lastDeliveryAt)
            return <span className="text-muted-foreground">{t('channels.neverDelivered')}</span>;
          const ok = lastDeliveryStatus === 'ok';
          return (
            <span className="inline-flex items-center gap-1.5">
              <SimpleTooltip content={ok ? t('channels.deliveryOk') : lastDeliveryStatus}>
                {ok ? (
                  <CircleCheckIcon
                    className="size-4 text-emerald-600 dark:text-emerald-400"
                    aria-label={t('channels.deliveryOk')}
                  />
                ) : (
                  <CircleXIcon
                    className="size-4 text-destructive"
                    aria-label={t('channels.deliveryFailed')}
                  />
                )}
              </SimpleTooltip>
              <TimeAgo value={lastDeliveryAt} past />
            </span>
          );
        },
      },
      {
        id: 'enabled',
        header: t('fields.enabled'),
        meta: { label: t('fields.enabled') },
        cell: ({ row }) => {
          const allowed = access(row.original.namespace, PERMISSIONS.notifyWrite);
          return (
            <SimpleTooltip
              content={
                allowed ? undefined : t('common:permission.missing', { permission: PERMISSIONS.notifyWrite })
              }
              enabled={!allowed}
            >
              <span className="inline-flex">
                <Switch
                  checked={row.original.enabled}
                  disabled={!allowed}
                  aria-label={t('channels.toggle', { name: row.original.name })}
                  onCheckedChange={(checked) => onToggle(row.original, checked)}
                />
              </span>
            </SimpleTooltip>
          );
        },
      },
      {
        id: 'actions',
        header: () => <span className="sr-only">{t('common:actions.more')}</span>,
        enableHiding: false,
        meta: { align: 'right' },
        cell: ({ row }) => {
          const allowed = access(row.original.namespace, PERMISSIONS.notifyWrite);
          return (
            <span className="inline-flex items-center gap-0.5">
              <RowButton
                allowed={allowed}
                label={t('channels.test')}
                disabled={testing.has(row.original.id)}
                onClick={() => onTest(row.original)}
              >
                <FlaskConicalIcon className={testing.has(row.original.id) ? 'animate-pulse' : undefined} />
              </RowButton>
              <RowButton
                allowed={allowed}
                label={t('common:actions.edit')}
                onClick={() => onEdit(row.original)}
              >
                <PencilIcon />
              </RowButton>
              <RowButton
                allowed={allowed}
                label={t('common:actions.delete')}
                destructive
                onClick={() => onDelete(row.original)}
              >
                <Trash2Icon />
              </RowButton>
            </span>
          );
        },
      },
    ],
    [t, access, onToggle, onTest, onEdit, onDelete, testing],
  );
}
