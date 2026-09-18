import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { ArrowRightLeftIcon, FolderTreeIcon, PencilIcon, PlusIcon, Trash2Icon } from 'lucide-react';
import { useCallback, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import { useAuth } from '@/app/auth/AuthContext';
import { checkPermission, PERMISSIONS } from '@/app/auth/permissions';
import { PermissionButton } from '@/app/auth/PermissionGate';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { DataTable, useCursorPagination } from '@/components/data-table';
import { EmptyState } from '@/components/EmptyState';
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { FormErrorAlert } from '@/features/access/components/FormErrorAlert';
import { RowActionButton } from '@/features/access/components/RowActionButton';
import { type Namespace } from '@/gen/spinneret/v1/auth_pb';
import { tenantClient } from '@/lib/clients';
import { errorMessage } from '@/lib/errors';
import { keepPreviousInScope, scopedKey } from '@/lib/queryKeys';

import { entityUpdateInit, type EntityFormValues } from '../validation';

import { EntityFormDialog } from './EntityFormDialog';
import { useEntityColumns } from './useEntityColumns';

type DialogState = { mode: 'create' } | { mode: 'edit'; namespace: Namespace } | undefined;

/** Namespaces of the active tenant (TenantAdminService namespace RPCs use the tenant header). */
export function NamespacesSection() {
  const { t } = useTranslation('tenants');
  const { tenantId, tenant } = useAuth();
  const tenantName = tenant?.tenant?.displayName || tenant?.tenant?.name || '';
  if (tenantId) return <NamespacesCard key={tenantId} tenantId={tenantId} tenantName={tenantName} />;
  return (
    <Card>
      <SectionHeader title={t('namespaces.titleNoTenant')} />
      <CardContent>
        <EmptyState
          compact
          icon={FolderTreeIcon}
          title={t('namespaces.noTenant')}
          description={t('namespaces.noTenantDescription')}
        />
      </CardContent>
    </Card>
  );
}

function SectionHeader({ title, action }: { title: string; action?: ReactNode }) {
  const { t } = useTranslation('tenants');
  return (
    <CardHeader className="flex-row items-start gap-3">
      <div className="grid gap-1">
        <CardTitle className="flex items-center gap-2">
          <FolderTreeIcon className="size-4 text-muted-foreground" aria-hidden />
          {title}
        </CardTitle>
        <CardDescription>{t('namespaces.description')}</CardDescription>
      </div>
      {action && <CardAction>{action}</CardAction>}
    </CardHeader>
  );
}

function NamespacesCard({ tenantId, tenantName }: { tenantId: string; tenantName: string }) {
  const { t } = useTranslation('tenants');
  const { isPlatformAdmin, tenant, namespaces, namespace: active, setNamespace, refresh } = useAuth();
  const queryClient = useQueryClient();
  const [dialog, setDialog] = useState<DialogState>();
  const [deleteTarget, setDeleteTarget] = useState<Namespace>();

  const pager = useCursorPagination({ resetOn: [tenantId] });
  const query = useQuery({
    // The list depends on the tenant only, not on the active namespace.
    queryKey: scopedKey('tenants', tenantId, undefined, 'namespaces', pager.pageToken, pager.pageSize),
    queryFn: ({ signal }) =>
      tenantClient.listNamespaces({ pageSize: pager.pageSize, pageToken: pager.pageToken }, { signal }),
    placeholderData: keepPreviousInScope(tenantId, undefined),
  });

  const canWrite = useCallback(
    (ns: Namespace) => {
      const access = namespaces.find((n) => n.namespace?.id === ns.id);
      return checkPermission(
        { isPlatformAdmin, namespace: access, bindings: tenant?.bindings },
        PERMISSIONS.namespaceWrite,
      );
    },
    [isPlatformAdmin, namespaces, tenant],
  );

  const afterChange = async () => {
    await queryClient.invalidateQueries({ queryKey: ['tenants'] });
    await refresh();
  };

  const save = useMutation({
    mutationFn: async (values: EntityFormValues): Promise<Namespace | undefined> => {
      if (dialog?.mode === 'edit') {
        const init = entityUpdateInit(dialog.namespace, values);
        return (await tenantClient.updateNamespace({ id: dialog.namespace.id, ...init })).namespace;
      }
      const response = await tenantClient.createNamespace({
        name: values.name,
        displayName: values.displayName,
        description: values.description,
      });
      return response.namespace;
    },
    onSuccess: async (saved) => {
      const key = dialog?.mode === 'create' ? 'namespaces.created' : 'namespaces.updated';
      setDialog(undefined);
      await afterChange();
      toast.success(t(key, { name: saved?.name ?? '' }));
    },
    onError: (err) => toast.error(errorMessage(err, t)),
  });

  const remove = useMutation({
    mutationFn: (ns: Namespace) => tenantClient.deleteNamespace({ id: ns.id }),
    onSuccess: async (_res, ns) => {
      toast.success(t('namespaces.deleted', { name: ns.name }));
      await afterChange();
    },
  });

  const resetSave = save.reset;
  const resetRemove = remove.reset;
  const activeId = active?.namespace?.id;
  const renderActions = useCallback(
    (ns: Namespace) => {
      const allowed = canWrite(ns);
      const accessible = namespaces.some((n) => n.namespace?.id === ns.id);
      return (
        <>
          <RowActionButton
            icon={ArrowRightLeftIcon}
            label={t('namespaces.switchTo')}
            disabled={ns.id === activeId || !accessible}
            disabledReason={
              ns.id === activeId ? t('namespaces.alreadyActive') : t('namespaces.notAccessible')
            }
            onClick={() => setNamespace(ns.name)}
          />
          <RowActionButton
            icon={PencilIcon}
            label={t('common:actions.edit')}
            allowed={allowed}
            permission={PERMISSIONS.namespaceWrite}
            onClick={() => {
              resetSave();
              setDialog({ mode: 'edit', namespace: ns });
            }}
          />
          <RowActionButton
            icon={Trash2Icon}
            label={t('common:actions.delete')}
            destructive
            allowed={allowed}
            permission={PERMISSIONS.namespaceWrite}
            onClick={() => {
              resetRemove();
              setDeleteTarget(ns);
            }}
          />
        </>
      );
    },
    [t, canWrite, namespaces, activeId, setNamespace, resetSave, resetRemove],
  );
  const columns = useEntityColumns<Namespace>({ activeId, renderActions });

  return (
    <Card>
      <SectionHeader
        title={t('namespaces.title', { tenant: tenantName })}
        action={
          <PermissionButton
            permission={PERMISSIONS.namespaceWrite}
            tenantLevel
            size="sm"
            onClick={() => {
              resetSave();
              setDialog({ mode: 'create' });
            }}
          >
            <PlusIcon />
            {t('namespaces.create')}
          </PermissionButton>
        }
      />
      <CardContent>
        <DataTable
          columns={columns}
          data={query.data?.namespaces}
          getRowId={(ns) => ns.id}
          isLoading={query.isLoading}
          isFetching={query.isFetching}
          error={query.error}
          onRetry={() => void query.refetch()}
          emptyTitle={t('namespaces.empty')}
          emptyDescription={t('namespaces.emptyDescription')}
          initialColumnVisibility={{ id: false, updatedAt: false }}
          pagination={{ pager, nextPageToken: query.data?.nextPageToken, total: query.data?.total }}
        />
      </CardContent>
      <EntityFormDialog
        kind="namespace"
        mode={dialog?.mode ?? 'create'}
        open={dialog !== undefined}
        onOpenChange={(open) => !open && setDialog(undefined)}
        initial={dialog?.mode === 'edit' ? dialog.namespace : undefined}
        tenantName={tenantName}
        pending={save.isPending}
        error={save.error}
        onSubmit={(values) => save.mutate(values)}
      />
      <ConfirmDialog
        open={deleteTarget !== undefined}
        onOpenChange={(open) => !open && setDeleteTarget(undefined)}
        destructive
        title={t('namespaces.deleteTitle', { name: deleteTarget?.name ?? '' })}
        description={t('namespaces.deleteDescription')}
        confirmText={deleteTarget?.name}
        confirmLabel={t('common:actions.delete')}
        onConfirm={() => (deleteTarget ? remove.mutateAsync(deleteTarget) : undefined)}
      >
        <FormErrorAlert error={remove.error} />
      </ConfirmDialog>
    </Card>
  );
}
