import { BanIcon } from 'lucide-react';
import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';

import { PERMISSIONS } from '@/app/auth/permissions';
import { IdText } from '@/components/CopyButton';
import { type DataTableColumn } from '@/components/data-table';
import { StateBadge } from '@/components/StateBadge';
import { TimeAgo } from '@/components/TimeAgo';
import { SimpleTooltip } from '@/components/ui/tooltip';
import { type ApiToken } from '@/gen/spinneret/v1/access_admin_pb';
import { formatNumber } from '@/lib/format';

import { tokenStatus, type TokenStatus } from '../../tokens';
import { RowActionButton } from '../RowActionButton';

import { ScopeChips } from './ScopeChips';

/** StateBadge state used for the color of each token status. */
const STATUS_TONE_STATE: Record<TokenStatus, string> = {
  active: 'active',
  expired: 'expired',
  revoked: 'banned',
};

export interface TokenColumnOptions {
  now: number;
  canWrite: boolean;
  onRevoke: (token: ApiToken) => void;
}

/** Column definitions of the tokens table. */
export function useTokenColumns({
  now,
  canWrite,
  onRevoke,
}: TokenColumnOptions): DataTableColumn<ApiToken>[] {
  const { t, i18n } = useTranslation('access');
  return useMemo<DataTableColumn<ApiToken>[]>(
    () => [
      {
        id: 'name',
        accessorKey: 'name',
        header: t('tokens.columns.name'),
        enableHiding: false,
        meta: { label: t('tokens.columns.name'), className: 'max-w-64' },
        cell: ({ row }) => (
          <div className="grid min-w-0">
            <span className="truncate font-medium">{row.original.name}</span>
            {row.original.description && (
              <span className="truncate text-xs text-muted-foreground" title={row.original.description}>
                {row.original.description}
              </span>
            )}
          </div>
        ),
      },
      {
        id: 'prefix',
        accessorKey: 'tokenPrefix',
        header: t('tokens.columns.prefix'),
        enableSorting: false,
        meta: { label: t('tokens.columns.prefix') },
        cell: ({ row }) => (
          <span className="font-mono text-xs" title={t('tokens.prefixHint')}>
            {row.original.tokenPrefix}…
          </span>
        ),
      },
      {
        id: 'scopes',
        header: t('tokens.columns.scopes'),
        enableSorting: false,
        meta: { label: t('tokens.columns.scopes'), className: 'min-w-48 whitespace-normal' },
        cell: ({ row }) => <ScopeChips scopes={row.original.scopes} />,
      },
      {
        id: 'ipAllowlist',
        header: t('tokens.columns.ipAllowlist'),
        enableSorting: false,
        meta: { label: t('tokens.columns.ipAllowlist') },
        cell: ({ row }) => {
          const list = row.original.ipAllowlist;
          if (list.length === 0) return <span className="text-muted-foreground">{t('tokens.anyIp')}</span>;
          const [first, ...rest] = list;
          return (
            <SimpleTooltip
              content={
                <span className="grid font-mono">
                  {list.map((ip) => (
                    <span key={ip}>{ip}</span>
                  ))}
                </span>
              }
            >
              <span
                tabIndex={0}
                className="font-mono text-xs outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                {first}
                {rest.length > 0 && (
                  <span className="text-muted-foreground"> {t('scopes.more', { count: rest.length })}</span>
                )}
              </span>
            </SimpleTooltip>
          );
        },
      },
      {
        id: 'rateLimit',
        accessorKey: 'rateLimitRps',
        header: t('tokens.columns.rateLimit'),
        meta: { label: t('tokens.columns.rateLimit'), align: 'right', className: 'tabular' },
        cell: ({ row }) =>
          row.original.rateLimitRps > 0 ? (
            t('tokens.rps', { value: formatNumber(row.original.rateLimitRps, undefined, i18n.language) })
          ) : (
            <span className="text-muted-foreground">{t('tokens.unlimited')}</span>
          ),
      },
      {
        id: 'status',
        header: t('tokens.columns.status'),
        meta: { label: t('tokens.columns.status') },
        accessorFn: (token) => tokenStatus(token, now),
        cell: ({ row }) => {
          const status = tokenStatus(row.original, now);
          return (
            <StateBadge
              kind="identity"
              state={STATUS_TONE_STATE[status]}
              label={t(`tokens.status.${status}`)}
            />
          );
        },
      },
      {
        id: 'expires',
        header: t('tokens.columns.expires'),
        meta: { label: t('tokens.columns.expires') },
        accessorFn: (token) => (token.expiresAt ? Number(token.expiresAt.seconds) : Number.MAX_SAFE_INTEGER),
        cell: ({ row }) => <TimeAgo value={row.original.expiresAt} fallback={t('tokens.never')} />,
      },
      {
        id: 'lastUsed',
        header: t('tokens.columns.lastUsed'),
        meta: { label: t('tokens.columns.lastUsed') },
        accessorFn: (token) => Number(token.lastUsedAt?.seconds ?? 0n),
        cell: ({ row }) => (
          <div className="grid">
            <TimeAgo value={row.original.lastUsedAt} fallback={t('tokens.neverUsed')} past />
            {row.original.lastUsedIp && (
              <span className="font-mono text-xs text-muted-foreground">{row.original.lastUsedIp}</span>
            )}
          </div>
        ),
      },
      {
        id: 'createdBy',
        accessorKey: 'createdBy',
        header: t('tokens.columns.createdBy'),
        meta: { label: t('tokens.columns.createdBy') },
        cell: ({ row }) => <IdText value={row.original.createdBy} truncate={20} />,
      },
      {
        id: 'createdAt',
        header: t('tokens.columns.createdAt'),
        meta: { label: t('tokens.columns.createdAt') },
        accessorFn: (token) => Number(token.createdAt?.seconds ?? 0n),
        cell: ({ row }) => <TimeAgo value={row.original.createdAt} past />,
      },
      {
        id: 'actions',
        header: () => <span className="sr-only">{t('tokens.columns.actions')}</span>,
        enableSorting: false,
        enableHiding: false,
        meta: { align: 'right' },
        cell: ({ row }) => {
          const revoked = tokenStatus(row.original, now) === 'revoked';
          return (
            <RowActionButton
              icon={BanIcon}
              label={t('tokens.revoke')}
              destructive
              allowed={canWrite}
              permission={PERMISSIONS.tokenWrite}
              disabled={revoked}
              disabledReason={t('tokens.alreadyRevoked')}
              onClick={() => onRevoke(row.original)}
            />
          );
        },
      },
    ],
    [t, i18n.language, now, canWrite, onRevoke],
  );
}
