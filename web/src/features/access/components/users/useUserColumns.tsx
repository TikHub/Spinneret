import { KeyRoundIcon, PencilIcon, ShieldCheckIcon, ShieldIcon } from 'lucide-react';
import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';

import { PERMISSIONS } from '@/app/auth/permissions';
import { IdText } from '@/components/CopyButton';
import { type DataTableColumn } from '@/components/data-table';
import { StateBadge } from '@/components/StateBadge';
import { TimeAgo } from '@/components/TimeAgo';
import { Badge } from '@/components/ui/badge';
import { type TenantUser } from '@/gen/spinneret/v1/access_admin_pb';
import { type User } from '@/gen/spinneret/v1/auth_pb';

import { RowActionButton } from '../RowActionButton';

import { BindingChip } from './BindingSummary';

/** Binding chips shown per row before summarizing. */
const MAX_BINDING_CHIPS = 3;

export interface UserColumnOptions {
  tenantId: string | undefined;
  canWrite: boolean;
  currentUserId: string | undefined;
  onEdit: (user: User) => void;
  onResetPassword: (user: User) => void;
  onBindings: (user: User) => void;
}

/** Column definitions of the users table. */
export function useUserColumns({
  tenantId,
  canWrite,
  currentUserId,
  onEdit,
  onResetPassword,
  onBindings,
}: UserColumnOptions): DataTableColumn<TenantUser>[] {
  const { t } = useTranslation('access');
  return useMemo<DataTableColumn<TenantUser>[]>(
    () => [
      {
        id: 'username',
        accessorFn: (row) => row.user?.username ?? '',
        header: t('users.columns.username'),
        enableHiding: false,
        meta: { label: t('users.columns.username') },
        cell: ({ row }) => {
          const user = row.original.user;
          return (
            <span className="flex items-center gap-1.5">
              <span className="font-mono text-sm font-medium">{user?.username}</span>
              {user?.isPlatformAdmin && (
                <Badge variant="secondary" title={t('users.platformAdminHint')}>
                  <ShieldCheckIcon />
                  {t('users.platformAdmin')}
                </Badge>
              )}
              {user?.id === currentUserId && <Badge variant="outline">{t('users.you')}</Badge>}
            </span>
          );
        },
      },
      {
        id: 'displayName',
        accessorFn: (row) => row.user?.displayName ?? '',
        header: t('users.columns.displayName'),
        meta: { label: t('users.columns.displayName'), className: 'max-w-48 truncate' },
        cell: ({ getValue }) =>
          getValue<string>() ? getValue<string>() : <span className="text-muted-foreground">—</span>,
      },
      {
        id: 'email',
        accessorFn: (row) => row.user?.email ?? '',
        header: t('users.columns.email'),
        meta: { label: t('users.columns.email'), className: 'max-w-56 truncate' },
        cell: ({ getValue }) =>
          getValue<string>() ? getValue<string>() : <span className="text-muted-foreground">—</span>,
      },
      {
        id: 'status',
        accessorFn: (row) => (row.user?.disabled ? 'disabled' : 'active'),
        header: t('users.columns.status'),
        meta: { label: t('users.columns.status') },
        cell: ({ getValue }) => {
          const state = getValue<'active' | 'disabled'>();
          return <StateBadge kind="account" state={state} label={t(`users.status.${state}`)} />;
        },
      },
      {
        id: 'bindings',
        header: t('users.columns.bindings'),
        enableSorting: false,
        meta: { label: t('users.columns.bindings'), className: 'min-w-56 whitespace-normal' },
        cell: ({ row }) => {
          const bindings = row.original.bindings;
          if (bindings.length === 0) {
            return <span className="text-xs text-muted-foreground">{t('users.noBindings')}</span>;
          }
          const hidden = bindings.length - MAX_BINDING_CHIPS;
          return (
            <span className="flex flex-wrap items-center gap-1">
              {bindings.slice(0, MAX_BINDING_CHIPS).map((binding) => (
                <BindingChip
                  key={binding.id}
                  binding={binding}
                  foreign={tenantId !== undefined && binding.tenantId !== tenantId}
                />
              ))}
              {hidden > 0 && (
                <span className="text-xs text-muted-foreground">{t('scopes.more', { count: hidden })}</span>
              )}
            </span>
          );
        },
      },
      {
        id: 'lastLogin',
        accessorFn: (row) => Number(row.user?.lastLoginAt?.seconds ?? 0n),
        header: t('users.columns.lastLogin'),
        meta: { label: t('users.columns.lastLogin') },
        cell: ({ row }) => (
          <TimeAgo value={row.original.user?.lastLoginAt} fallback={t('common:time.never')} past />
        ),
      },
      {
        id: 'createdAt',
        accessorFn: (row) => Number(row.user?.createdAt?.seconds ?? 0n),
        header: t('users.columns.createdAt'),
        meta: { label: t('users.columns.createdAt') },
        cell: ({ row }) => <TimeAgo value={row.original.user?.createdAt} past />,
      },
      {
        id: 'id',
        accessorFn: (row) => row.user?.id ?? '',
        header: t('users.columns.id'),
        meta: { label: t('users.columns.id') },
        cell: ({ getValue }) => <IdText value={getValue<string>()} />,
      },
      {
        id: 'actions',
        header: () => <span className="sr-only">{t('users.columns.actions')}</span>,
        enableSorting: false,
        enableHiding: false,
        meta: { align: 'right' },
        cell: ({ row }) => {
          const user = row.original.user;
          if (!user) return null;
          return (
            <span className="inline-flex items-center gap-0.5">
              <RowActionButton
                icon={ShieldIcon}
                label={t('users.actions.bindings')}
                onClick={() => onBindings(user)}
              />
              <RowActionButton
                icon={PencilIcon}
                label={t('users.actions.edit')}
                allowed={canWrite}
                permission={PERMISSIONS.userWrite}
                onClick={() => onEdit(user)}
              />
              <RowActionButton
                icon={KeyRoundIcon}
                label={t('users.actions.resetPassword')}
                allowed={canWrite}
                permission={PERMISSIONS.userWrite}
                onClick={() => onResetPassword(user)}
              />
            </span>
          );
        },
      },
    ],
    [t, tenantId, canWrite, currentUserId, onEdit, onResetPassword, onBindings],
  );
}
