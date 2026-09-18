import { LockIcon } from 'lucide-react';
import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';

import { useAuth } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';
import { DataTable, useCursorPagination, type DataTableColumn } from '@/components/data-table';
import { EmptyState } from '@/components/EmptyState';
import { TimeAgo } from '@/components/TimeAgo';
import { Badge } from '@/components/ui/badge';
import { type SecretAccessLog, type SecretInfo } from '@/gen/spinneret/v1/secret_admin_pb';

import { useSecretAccessLogs } from '../useSecretsApi';

const RESULT_CLASS: Record<string, string> = {
  ok: 'border-emerald-500/25 bg-emerald-500/10 text-emerald-700 dark:text-emerald-400',
  denied: 'border-amber-500/25 bg-amber-500/10 text-amber-700 dark:text-amber-400',
  error: 'border-rose-500/25 bg-rose-500/10 text-rose-700 dark:text-rose-400',
};

/** Audited reads, reveals and changes of a secret (requires audit:read as well). */
export function SecretAccessLogsTab({ secret }: { secret: SecretInfo }) {
  const { t } = useTranslation('secrets');
  const { tenantId, namespaceName, can } = useAuth();
  const allowed = can(PERMISSIONS.auditRead);
  const pager = useCursorPagination({ pageSize: 25, resetOn: [tenantId, namespaceName, secret.id] });
  const logs = useSecretAccessLogs(secret.id, pager, allowed);

  const columns = useMemo<DataTableColumn<SecretAccessLog>[]>(
    () => [
      {
        id: 'time',
        header: t('accessLogs.time'),
        meta: { label: t('accessLogs.time') },
        cell: ({ row }) => <TimeAgo value={row.original.createdAt} past />,
      },
      {
        id: 'actor',
        header: t('accessLogs.actor'),
        meta: { label: t('accessLogs.actor') },
        cell: ({ row }) => {
          const log = row.original;
          return (
            <span className="grid">
              <span className="truncate">{log.actorName || log.actorId || '—'}</span>
              <span className="font-mono text-[11px] text-muted-foreground">
                {t(`accessLogs.actorKinds.${log.actorKind}`, { defaultValue: log.actorKind })}
                {log.actorId && ` · ${log.actorId}`}
              </span>
            </span>
          );
        },
      },
      {
        id: 'action',
        header: t('accessLogs.action'),
        meta: { label: t('accessLogs.action') },
        cell: ({ row }) => <span className="font-mono text-xs">{row.original.action}</span>,
      },
      {
        id: 'version',
        header: t('accessLogs.version'),
        meta: { label: t('accessLogs.version'), align: 'right' },
        cell: ({ row }) =>
          row.original.version > 0 ? <span className="tabular">v{row.original.version}</span> : '—',
      },
      {
        id: 'result',
        header: t('accessLogs.result'),
        meta: { label: t('accessLogs.result') },
        cell: ({ row }) => (
          <Badge variant="outline" className={RESULT_CLASS[row.original.result]}>
            {t(`accessLogs.results.${row.original.result}`, { defaultValue: row.original.result })}
          </Badge>
        ),
      },
      {
        id: 'ip',
        header: t('accessLogs.ip'),
        meta: { label: t('accessLogs.ip') },
        cell: ({ row }) => <span className="font-mono text-xs">{row.original.ip || '—'}</span>,
      },
    ],
    [t],
  );

  if (!allowed) {
    return (
      <EmptyState
        compact
        icon={LockIcon}
        title={t('common:permission.deniedTitle')}
        description={t('common:permission.missing', { permission: PERMISSIONS.auditRead })}
      />
    );
  }

  return (
    <DataTable
      columns={columns}
      data={logs.data?.logs}
      getRowId={(log, i) => `${log.createdAt?.seconds ?? 0}-${log.createdAt?.nanos ?? 0}-${log.action}-${i}`}
      isLoading={logs.isLoading}
      isFetching={logs.isFetching}
      error={logs.error}
      onRetry={() => void logs.refetch()}
      emptyTitle={t('accessLogs.empty')}
      enableColumnVisibility={false}
      pagination={{ pager, nextPageToken: logs.data?.nextPageToken, pageSizeOptions: [25, 50, 100] }}
    />
  );
}
