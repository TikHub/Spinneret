import { useQuery } from '@tanstack/react-query';
import { PlusIcon, RefreshCwIcon } from 'lucide-react';
import { useCallback, useId, useState } from 'react';
import { useTranslation } from 'react-i18next';

import { useAuth, useScopedPlaceholder, useScopedQueryKey } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';
import { PermissionButton, RequirePermission } from '@/app/auth/PermissionGate';
import { DataTable, useCursorPagination } from '@/components/data-table';
import { FilterBar } from '@/components/FilterBar';
import { PageHeader } from '@/components/PageHeader';
import { PageIntro } from '@/components/PageIntro';
import { Button } from '@/components/ui/button';
import { Label } from '@/components/ui/label';
import { Switch } from '@/components/ui/switch';
import { type User } from '@/gen/spinneret/v1/auth_pb';
import { accessClient } from '@/lib/clients';

import { BindingsSheet } from '../components/users/BindingsSheet';
import { CreateUserDialog } from '../components/users/CreateUserDialog';
import { EditUserDialog } from '../components/users/EditUserDialog';
import { ResetPasswordDialog } from '../components/users/ResetPasswordDialog';
import { useUserColumns } from '../components/users/useUserColumns';

/** Members of the active tenant with their role bindings. */
export default function UsersPage() {
  return (
    <RequirePermission permission={PERMISSIONS.userRead} tenantLevel>
      <UsersContent />
    </RequirePermission>
  );
}

function UsersContent() {
  const { t } = useTranslation('access');
  const { tenantId, tenant, namespaceName, isPlatformAdmin, canInTenant, user: me } = useAuth();
  const key = useScopedQueryKey();
  const keepPrevious = useScopedPlaceholder();
  const allUsersId = useId();
  const canWrite = canInTenant(PERMISSIONS.userWrite);
  const tenantName = tenant?.tenant?.displayName || tenant?.tenant?.name || '';

  const [search, setSearch] = useState('');
  const [allUsers, setAllUsers] = useState(false);
  const [createOpen, setCreateOpen] = useState(false);
  const [editUser, setEditUser] = useState<User>();
  const [resetUser, setResetUser] = useState<User>();
  const [bindingsUser, setBindingsUser] = useState<User>();

  const filters = { query: search.trim(), allUsers: isPlatformAdmin && allUsers };
  const pager = useCursorPagination({ resetOn: [tenantId, namespaceName, filters] });
  const query = useQuery({
    queryKey: key('access', 'users', filters, pager.pageToken, pager.pageSize),
    queryFn: ({ signal }) =>
      accessClient.listUsers(
        { ...filters, pageSize: pager.pageSize, pageToken: pager.pageToken },
        { signal },
      ),
    enabled: Boolean(tenantId),
    placeholderData: keepPrevious,
  });

  const onEdit = useCallback((user: User) => setEditUser(user), []);
  const onResetPassword = useCallback((user: User) => setResetUser(user), []);
  const onBindings = useCallback((user: User) => setBindingsUser(user), []);
  const columns = useUserColumns({
    tenantId,
    canWrite,
    currentUserId: me?.id,
    onEdit,
    onResetPassword,
    onBindings,
  });

  return (
    <>
      <PageHeader
        title={t('users.title')}
        description={t('users.description', { tenant: tenantName })}
        actions={
          <>
            <Button
              variant="outline"
              size="icon"
              onClick={() => void query.refetch()}
              aria-label={t('common:actions.refresh')}
            >
              <RefreshCwIcon className={query.isFetching ? 'animate-spin' : undefined} />
            </Button>
            <PermissionButton
              permission={PERMISSIONS.userWrite}
              tenantLevel
              onClick={() => setCreateOpen(true)}
            >
              <PlusIcon />
              {t('users.create.button')}
            </PermissionButton>
          </>
        }
      />
      <PageIntro
        page="users"
        links={[
          { to: '/admin/tenants', labelKey: 'nav.tenants' },
          { to: '/access/audit', labelKey: 'nav.audit' },
        ]}
      />
      <DataTable
        columns={columns}
        data={query.data?.users}
        getRowId={(row) => row.user?.id ?? ''}
        isLoading={query.isLoading}
        isFetching={query.isFetching}
        error={query.error}
        onRetry={() => void query.refetch()}
        emptyTitle={filters.query ? t('users.noMatches') : t('users.empty')}
        emptyDescription={filters.query ? t('users.noMatchesDescription') : t('users.emptyDescription')}
        onRowClick={(row) => row.user && setBindingsUser(row.user)}
        rowClassName={(row) => (row.user?.disabled ? 'opacity-70' : undefined)}
        initialColumnVisibility={{ createdAt: false, id: false }}
        pagination={{ pager, nextPageToken: query.data?.nextPageToken, total: query.data?.total }}
        toolbar={
          <FilterBar
            search={{ value: search, onChange: setSearch, placeholder: t('users.searchPlaceholder') }}
            activeCount={(filters.query ? 1 : 0) + (filters.allUsers ? 1 : 0)}
            onReset={() => {
              setSearch('');
              setAllUsers(false);
            }}
          >
            {isPlatformAdmin && (
              <div className="flex items-center gap-2">
                <Switch id={allUsersId} checked={allUsers} onCheckedChange={setAllUsers} />
                <Label htmlFor={allUsersId} className="text-sm font-normal">
                  {t('users.allUsers')}
                </Label>
              </div>
            )}
          </FilterBar>
        }
      />
      <CreateUserDialog open={createOpen} onOpenChange={setCreateOpen} tenantName={tenantName} />
      <EditUserDialog user={editUser} onClose={() => setEditUser(undefined)} />
      <ResetPasswordDialog user={resetUser} onClose={() => setResetUser(undefined)} />
      <BindingsSheet user={bindingsUser} onClose={() => setBindingsUser(undefined)} />
    </>
  );
}
