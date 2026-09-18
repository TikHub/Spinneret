import { Link } from '@tanstack/react-router';
import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';

import { CopyButton } from '@/components/CopyButton';
import { type DataTableColumn } from '@/components/data-table';
import { TimeAgo } from '@/components/TimeAgo';
import { type Identity } from '@/gen/spinneret/v1/identity_admin_pb';
import { formatNumber } from '@/lib/format';
import { toDate } from '@/lib/time';

import { Muted, StateCell, TagsCell } from './IdentityCells';
import { ScoreBar } from './ScoreBar';

/** Columns of the identities table; sortable columns map to ListIdentities.order_by. */
export function useIdentityColumns(): DataTableColumn<Identity>[] {
  const { t, i18n } = useTranslation('identities');
  const lng = i18n.language;
  return useMemo<DataTableColumn<Identity>[]>(
    () => [
      {
        id: 'id',
        header: t('columns.id'),
        enableSorting: false,
        enableHiding: false,
        meta: { label: t('columns.id') },
        cell: ({ row }) => (
          <span className="inline-flex items-center gap-0.5">
            <Link
              to="/identities/$id"
              params={{ id: row.original.id }}
              className="font-mono text-xs hover:underline"
              title={row.original.id}
            >
              {row.original.id}
            </Link>
            <CopyButton value={row.original.id} />
          </span>
        ),
      },
      {
        id: 'site',
        header: t('columns.site'),
        enableSorting: false,
        meta: { label: t('columns.site') },
        cell: ({ row }) => <Muted value={row.original.site} />,
      },
      {
        id: 'client',
        header: t('columns.client'),
        enableSorting: false,
        meta: { label: t('columns.client') },
        cell: ({ row }) => <Muted value={row.original.client} />,
      },
      {
        id: 'type',
        header: t('columns.type'),
        enableSorting: false,
        meta: { label: t('columns.type'), className: 'font-mono text-xs' },
        cell: ({ row }) => <Muted value={row.original.type} />,
      },
      {
        id: 'state',
        header: t('columns.state'),
        enableSorting: false,
        meta: { label: t('columns.state') },
        cell: ({ row }) => <StateCell identity={row.original} />,
      },
      {
        id: 'account',
        header: t('columns.account'),
        enableSorting: false,
        meta: { label: t('columns.account'), className: 'max-w-40 truncate font-mono text-xs' },
        cell: ({ row }) => <Muted value={row.original.accountRef} />,
      },
      {
        id: 'region',
        header: t('columns.region'),
        enableSorting: false,
        meta: { label: t('columns.region') },
        cell: ({ row }) => <Muted value={row.original.region} />,
      },
      {
        id: 'tags',
        header: t('columns.tags'),
        enableSorting: false,
        meta: { label: t('columns.tags') },
        cell: ({ row }) => <TagsCell tags={row.original.tags} />,
      },
      {
        id: 'globalScore',
        accessorFn: (row) => row.globalScore,
        header: t('columns.globalScore'),
        sortDescFirst: true,
        meta: { label: t('columns.globalScore') },
        cell: ({ row }) => <ScoreBar score={row.original.globalScore} samples={row.original.globalSamples} />,
      },
      {
        id: 'activeLeases',
        header: t('columns.activeLeases'),
        enableSorting: false,
        meta: { label: t('columns.activeLeases'), align: 'right', className: 'tabular' },
        cell: ({ row }) => formatNumber(row.original.activeLeases, undefined, lng),
      },
      {
        id: 'payloadVersion',
        header: t('columns.payloadVersion'),
        enableSorting: false,
        meta: { label: t('columns.payloadVersion'), align: 'right', className: 'tabular' },
        cell: ({ row }) => `v${row.original.payloadVersion}`,
      },
      {
        id: 'lastUsedAt',
        accessorFn: (row) => toDate(row.lastUsedAt)?.getTime() ?? 0,
        header: t('columns.lastUsed'),
        sortDescFirst: true,
        meta: { label: t('columns.lastUsed') },
        cell: ({ row }) => <TimeAgo value={row.original.lastUsedAt} fallback={t('common:time.never')} past />,
      },
      {
        id: 'stateChangedAt',
        accessorFn: (row) => toDate(row.stateChangedAt)?.getTime() ?? 0,
        header: t('columns.stateChanged'),
        sortDescFirst: true,
        meta: { label: t('columns.stateChanged') },
        cell: ({ row }) => <TimeAgo value={row.original.stateChangedAt} past />,
      },
    ],
    [t, lng],
  );
}
