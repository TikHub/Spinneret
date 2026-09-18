import { Link } from '@tanstack/react-router';
import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';

import { IdText } from '@/components/CopyButton';
import { type DataTableColumn } from '@/components/data-table';
import { StateBadge } from '@/components/StateBadge';
import { TimeAgo } from '@/components/TimeAgo';
import { SimpleTooltip } from '@/components/ui/tooltip';
import { Countdown } from '@/features/identities/components/Countdown';
import { Muted, TagsCell } from '@/features/identities/components/IdentityCells';
import { type Account } from '@/gen/spinneret/v1/identity_admin_pb';
import { formatNumber } from '@/lib/format';
import { toDate } from '@/lib/time';

import { type AccountOperation } from '../accounts';
import { AccountActionsMenu } from './AccountActionsMenu';

export interface AccountColumnsOptions {
  onOperate: (operation: AccountOperation, account: Account) => void;
  onEdit: (account: Account) => void;
}

/** Columns of the accounts table. */
export function useAccountColumns({ onOperate, onEdit }: AccountColumnsOptions): DataTableColumn<Account>[] {
  const { t, i18n } = useTranslation('accounts');
  const lng = i18n.language;
  return useMemo<DataTableColumn<Account>[]>(
    () => [
      {
        id: 'externalRef',
        header: t('columns.externalRef'),
        enableSorting: false,
        enableHiding: false,
        meta: { label: t('columns.externalRef') },
        cell: ({ row }) => (
          <div className="grid gap-0.5">
            <span className="font-mono text-xs font-medium">{row.original.externalRef}</span>
            <IdText value={row.original.id} className="text-muted-foreground" />
          </div>
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
        id: 'state',
        header: t('columns.state'),
        enableSorting: false,
        meta: { label: t('columns.state') },
        cell: ({ row }) => {
          const account = row.original;
          const banned = account.state === 'banned';
          return (
            <span className="inline-flex items-center gap-2">
              <StateBadge kind="account" state={account.state} />
              {banned && !toDate(account.banUntil) && (
                <span className="text-xs text-rose-600 dark:text-rose-400">{t('state.permanent')}</span>
              )}
            </span>
          );
        },
      },
      {
        id: 'banUntil',
        header: t('columns.banUntil'),
        enableSorting: false,
        meta: { label: t('columns.banUntil') },
        cell: ({ row }) => (
          <Countdown until={row.original.state === 'banned' ? row.original.banUntil : undefined} />
        ),
      },
      {
        id: 'cooldownUntil',
        header: t('columns.cooldownUntil'),
        enableSorting: false,
        meta: { label: t('columns.cooldownUntil') },
        cell: ({ row }) => <Countdown until={row.original.cooldownUntil} />,
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
        id: 'identityCount',
        header: t('columns.identities'),
        enableSorting: false,
        meta: { label: t('columns.identities'), align: 'right', className: 'tabular' },
        cell: ({ row }) => (
          <Link
            to="/identities"
            search={{ site: row.original.site, account_ref: row.original.externalRef }}
            className="hover:underline"
          >
            {formatNumber(row.original.identityCount, undefined, lng)}
          </Link>
        ),
      },
      {
        id: 'notes',
        header: t('columns.notes'),
        enableSorting: false,
        meta: { label: t('columns.notes') },
        cell: ({ row }) =>
          row.original.notes ? (
            <SimpleTooltip content={<span className="whitespace-pre-wrap">{row.original.notes}</span>}>
              <span className="block max-w-48 truncate text-muted-foreground">{row.original.notes}</span>
            </SimpleTooltip>
          ) : (
            <Muted value="" />
          ),
      },
      {
        id: 'updatedAt',
        header: t('columns.updated'),
        enableSorting: false,
        meta: { label: t('columns.updated') },
        cell: ({ row }) => <TimeAgo value={row.original.updatedAt} past />,
      },
      {
        id: 'actions',
        header: () => <span className="sr-only">{t('columns.actions')}</span>,
        enableSorting: false,
        enableHiding: false,
        meta: { align: 'right' },
        cell: ({ row }) => (
          <AccountActionsMenu account={row.original} onOperate={onOperate} onEdit={onEdit} />
        ),
      },
    ],
    [t, lng, onOperate, onEdit],
  );
}
