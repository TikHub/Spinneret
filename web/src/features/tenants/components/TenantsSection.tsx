import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { ArrowRightLeftIcon, Building2Icon, PencilIcon, PlusIcon, Trash2Icon } from 'lucide-react';
import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import { useAuth } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';
import { PermissionButton } from '@/app/auth/PermissionGate';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { DataTable, useCursorPagination } from '@/components/data-table';
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { FormErrorAlert } from '@/features/access/components/FormErrorAlert';
import { RowActionButton } from '@/features/access/components/RowActionButton';
import { type Tenant } from '@/gen/spinneret/v1/auth_pb';
import { tenantClient } from '@/lib/clients';
import { errorMessage } from '@/lib/errors';
import { keepPreviousInScope, scopedKey } from '@/lib/queryKeys';

import { entityUpdateInit, type EntityFormValues } from '../validation';

import { EntityFormDialog } from './EntityFormDialog';
import { useEntityColumns } from './useEntityColumns';

/** The tenant list does not depend on the active tenant or namespace. */
const keepPreviousTenants = keepPreviousInScope(undefined, undefined);

type DialogState = { mode: 'create' } | { mode: 'edit'; tenant: Tenant } | undefined;

/** Tenants table: every tenant for platform admins, the caller's tenants otherwise. */
export function TenantsSection() {
  const { t } = useTranslation('tenants');
  const { tenantId, tenants, can, setTenant, refresh, user } = useAuth();
  const queryClient = useQueryClient();
  const canManage = can(PERMISSIONS.tenantManage);
  const [dialog, setDialog] = useState<DialogState>();
  const [deleteTarget, setDeleteTarget] = useState<Tenant>();

  // Toast actions run after a refresh; they must use the latest tenant list.
  const setTenantRef = useRef(setTenant);
  useEffect(() => {
    setTenantRef.current = setTenant;
  }, [setTenant]);

  const pager = useCursorPagination({ resetOn: [user?.id] });
  const query = useQuery({
    queryKey: scopedKey('tenants', undefined, undefined, 'list', pager.pageToken, pager.pageSize),
    queryFn: ({ signal }) =>
      tenantClient.listTenants({ pageSize: pager.pageSize, pageToken: pager.pageToken }, { signal }),
    placeholderData: keepPreviousTenants,
  });

  const afterChange = async () => {
    await queryClient.invalidateQueries({ queryKey: ['tenants'] });
    await refresh();
  };

  const save = useMutation({
    mutationFn: async (values: EntityFormValues): Promise<Tenant | undefined> => {
      if (dialog?.mode === 'edit') {
        const init = entityUpdateInit(dialog.tenant, values);
        return (await tenantClient.updateTenant({ id: dialog.tenant.id, ...init })).tenant;
      }
      const response = await tenantClient.createTenant({
        name: values.name,
        displayName: values.displayName,
        description: values.description,
        ownerUserId: values.ownerUserId,
      });
      return response.tenant;
    },
    onSuccess: async (saved) => {
      const created = dialog?.mode === 'create';
      setDialog(undefined);
      await afterChange();
      if (created && saved) {
        toast.success(t('tenants.created', { name: saved.name }), {
          action: { label: t('tenants.switchTo'), onClick: () => setTenantRef.current(saved.id) },
        });
      } else {
        toast.success(t('tenants.updated', { name: saved?.name ?? '' }));
      }
    },
    onError: (err) => toast.error(errorMessage(err, t)),
  });

  const remove = useMutation({
    mutationFn: (tenant: Tenant) => tenantClient.deleteTenant({ id: tenant.id }),
    onSuccess: async (_res, tenant) => {
      toast.success(t('tenants.deleted', { name: tenant.name }));
      await afterChange();
    },
  });

  const resetSave = save.reset;
  const resetRemove = remove.reset;
  const accessible = useCallback(
    (id: string) => tenants.some((access) => access.tenant?.id === id),
    [tenants],
  );

  const renderActions = useCallback(
    (tenant: Tenant) => (
      <>
        <RowActionButton
          icon={ArrowRightLeftIcon}
          label={t('tenants.switchTo')}
          disabled={tenant.id === tenantId || !accessible(tenant.id)}
          disabledReason={tenant.id === tenantId ? t('tenants.alreadyActive') : t('tenants.notAccessible')}
          onClick={() => setTenant(tenant.id)}
        />
        <RowActionButton
          icon={PencilIcon}
          label={t('common:actions.edit')}
          allowed={canManage}
          permission={PERMISSIONS.tenantManage}
          onClick={() => {
            resetSave();
            setDialog({ mode: 'edit', tenant });
          }}
        />
        <RowActionButton
          icon={Trash2Icon}
          label={t('common:actions.delete')}
          destructive
          allowed={canManage}
          permission={PERMISSIONS.tenantManage}
          onClick={() => {
            resetRemove();
            setDeleteTarget(tenant);
          }}
        />
      </>
    ),
    [t, tenantId, accessible, canManage, setTenant, resetSave, resetRemove],
  );
  const columns = useEntityColumns<Tenant>({ activeId: tenantId, renderActions });

  const openCreate = () => {
    resetSave();
    setDialog({ mode: 'create' });
  };

  return (
    <Card>
      <CardHeader className="flex-row items-start gap-3">
        <div className="grid gap-1">
          <CardTitle className="flex items-center gap-2">
            <Building2Icon className="size-4 text-muted-foreground" aria-hidden />
            {t('tenants.title')}
          </CardTitle>
          <CardDescription>
            {canManage ? t('tenants.descriptionAdmin') : t('tenants.description')}
          </CardDescription>
        </div>
        <CardAction>
          <PermissionButton permission={PERMISSIONS.tenantManage} size="sm" onClick={openCreate}>
            <PlusIcon />
            {t('tenants.create')}
          </PermissionButton>
        </CardAction>
      </CardHeader>
      <CardContent>
        <DataTable
          columns={columns}
          data={query.data?.tenants}
          getRowId={(tenant) => tenant.id}
          isLoading={query.isLoading}
          isFetching={query.isFetching}
          error={query.error}
          onRetry={() => void query.refetch()}
          emptyTitle={t('tenants.empty')}
          emptyDescription={canManage ? t('tenants.emptyAdminDescription') : t('tenants.emptyDescription')}
          onRowClick={(tenant) => {
            if (tenant.id !== tenantId && accessible(tenant.id)) setTenant(tenant.id);
          }}
          initialColumnVisibility={{ id: false, updatedAt: false }}
          pagination={{ pager, nextPageToken: query.data?.nextPageToken, total: query.data?.total }}
        />
      </CardContent>

      <EntityFormDialog
        kind="tenant"
        mode={dialog?.mode ?? 'create'}
        open={dialog !== undefined}
        onOpenChange={(open) => !open && setDialog(undefined)}
        initial={dialog?.mode === 'edit' ? dialog.tenant : undefined}
        pending={save.isPending}
        error={save.error}
        onSubmit={(values) => save.mutate(values)}
      />
      <ConfirmDialog
        open={deleteTarget !== undefined}
        onOpenChange={(open) => !open && setDeleteTarget(undefined)}
        destructive
        title={t('tenants.deleteTitle', { name: deleteTarget?.name ?? '' })}
        description={t('tenants.deleteDescription')}
        confirmText={deleteTarget?.name}
        confirmLabel={t('common:actions.delete')}
        onConfirm={() => (deleteTarget ? remove.mutateAsync(deleteTarget) : undefined)}
      >
        <FormErrorAlert error={remove.error} />
      </ConfirmDialog>
    </Card>
  );
}
