import { KeyRoundIcon } from 'lucide-react';
import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';

import { useAuth } from '@/app/auth/AuthContext';
import { DataTable, useCursorPagination, type DataTableColumn } from '@/components/data-table';
import { TimeAgo } from '@/components/TimeAgo';
import { Badge } from '@/components/ui/badge';
import { type SecretInfo, type SecretVersion } from '@/gen/spinneret/v1/secret_admin_pb';

import { useSecretVersions } from '../useSecretsApi';

/** Version metadata of a secret with the KEK wrapping each version's data key. */
export function SecretVersionsTab({ secret }: { secret: SecretInfo }) {
  const { t } = useTranslation('secrets');
  const { tenantId, namespaceName } = useAuth();
  const pager = useCursorPagination({ pageSize: 25, resetOn: [tenantId, namespaceName, secret.id] });
  const versions = useSecretVersions(secret.id, pager);

  const columns = useMemo<DataTableColumn<SecretVersion>[]>(
    () => [
      {
        id: 'version',
        header: t('versions.version'),
        meta: { label: t('versions.version') },
        cell: ({ row }) => (
          <span className="inline-flex items-center gap-1.5">
            <span className="font-mono tabular">v{row.original.version}</span>
            {row.original.version === secret.currentVersion && (
              <Badge variant="secondary">{t('versions.current')}</Badge>
            )}
          </span>
        ),
      },
      {
        id: 'kek',
        header: t('versions.kek'),
        meta: { label: t('versions.kek') },
        cell: ({ row }) => (
          <span className="inline-flex items-center gap-1 font-mono text-xs">
            <KeyRoundIcon className="size-3 text-muted-foreground" aria-hidden />
            {row.original.kekId || '—'}
          </span>
        ),
      },
      {
        id: 'createdBy',
        header: t('versions.createdBy'),
        meta: { label: t('versions.createdBy') },
        cell: ({ row }) => <span className="font-mono text-xs">{row.original.createdBy || '—'}</span>,
      },
      {
        id: 'createdAt',
        header: t('versions.createdAt'),
        meta: { label: t('versions.createdAt') },
        cell: ({ row }) => <TimeAgo value={row.original.createdAt} past />,
      },
    ],
    [t, secret.currentVersion],
  );

  return (
    <DataTable
      columns={columns}
      data={versions.data?.versions}
      getRowId={(v) => String(v.version)}
      isLoading={versions.isLoading}
      isFetching={versions.isFetching}
      error={versions.error}
      onRetry={() => void versions.refetch()}
      emptyTitle={t('versions.empty')}
      enableColumnVisibility={false}
      pagination={{
        pager,
        nextPageToken: versions.data?.nextPageToken,
        total: versions.data?.total,
        pageSizeOptions: [25, 50, 100],
      }}
    />
  );
}
