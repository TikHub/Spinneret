import { useQuery } from '@tanstack/react-query';
import { useNavigate, useSearch } from '@tanstack/react-router';
import { type RowSelectionState, type SortingState, type Updater } from '@tanstack/react-table';
import { FingerprintIcon, LayersIcon, PlusIcon, Undo2Icon, UploadIcon } from 'lucide-react';
import { useCallback, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';

import { useAuth, useScopedPlaceholder, useScopedQueryKey } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';
import { PermissionButton, RequirePermission } from '@/app/auth/PermissionGate';
import { LIVE_REFETCH_MS } from '@/app/queryClient';
import { DataTable, useCursorPagination } from '@/components/data-table';
import { EmptyState } from '@/components/EmptyState';
import { PageHeader } from '@/components/PageHeader';
import { PageIntro } from '@/components/PageIntro';
import { type BulkResult } from '@/gen/spinneret/v1/common_pb';
import { type Identity } from '@/gen/spinneret/v1/identity_admin_pb';
import { identityClient } from '@/lib/clients';

import { BulkFilterOperationDialog } from '../components/BulkFilterOperationDialog';
import { BulkResultDialog } from '../components/BulkResultDialog';
import { IdentityFilterBar } from '../components/IdentityFilterBar';
import { ImportDialog } from '../components/ImportDialog';
import { NewIdentityDialog } from '../components/NewIdentityDialog';
import { OperationDialog } from '../components/OperationDialog';
import { OperationsMenu } from '../components/OperationsMenu';
import { RevertActionsDialog } from '../components/RevertActionsDialog';
import { SelectionActions } from '../components/SelectionActions';
import { useIdentityColumns } from '../components/useIdentityColumns';
import {
  countActiveFilters,
  mergeIdentitySearch,
  parseIdentitySearch,
  sortingFromParams,
  sortParamsFromSorting,
  toIdentityFilter,
  type IdentityListParams,
} from '../identitySearch';
import { notifyBulkResult, useInvalidateIdentityData } from '../notify';
import { IDENTITY_OPERATIONS, type IdentityOperation } from '../operations';

type DialogState =
  | { kind: 'operate'; operation: IdentityOperation; ids: string[]; site?: string }
  | { kind: 'bulk'; operation: IdentityOperation }
  | { kind: 'import' }
  | { kind: 'new' }
  | { kind: 'revert' }
  | { kind: 'result'; title: string; result: BulkResult | undefined };

function IdentitiesContent() {
  const { t } = useTranslation('identities');
  const { namespaceName, tenantId } = useAuth();
  const search = useSearch({ from: '/_app/identities' });
  const navigate = useNavigate({ from: '/identities' });
  const key = useScopedQueryKey();
  const keepPrevious = useScopedPlaceholder();
  const invalidate = useInvalidateIdentityData();
  const columns = useIdentityColumns();

  const params = useMemo(() => parseIdentitySearch(search), [search]);
  const filter = useMemo(() => toIdentityFilter(params), [params]);
  const [dialog, setDialog] = useState<DialogState | null>(null);
  const [dialogSeq, setDialogSeq] = useState(0);

  const setParams = useCallback(
    (next: IdentityListParams) => {
      void navigate({ search: (prev) => mergeIdentitySearch(prev, next), replace: true });
    },
    [navigate],
  );

  const scopeKey = [tenantId, namespaceName, filter, params.orderBy, params.descending];
  const pager = useCursorPagination({ resetOn: scopeKey });
  const selectionKey = JSON.stringify([...scopeKey, pager.pageToken, pager.pageSize]);
  const [selection, setSelection] = useState<{ key: string; value: RowSelectionState }>({
    key: '',
    value: {},
  });
  const rowSelection = selection.key === selectionKey ? selection.value : {};
  const onRowSelectionChange = (updater: Updater<RowSelectionState>) =>
    setSelection({
      key: selectionKey,
      value: typeof updater === 'function' ? updater(rowSelection) : updater,
    });
  const clearSelection = () => setSelection({ key: selectionKey, value: {} });

  const query = useQuery({
    queryKey: key(
      'identities',
      'list',
      filter,
      params.orderBy,
      params.descending,
      pager.pageToken,
      pager.pageSize,
    ),
    queryFn: ({ signal }) =>
      identityClient.listIdentities(
        {
          namespace: namespaceName ?? '',
          filter,
          orderBy: params.orderBy,
          descending: params.descending,
          pageSize: pager.pageSize,
          pageToken: pager.pageToken,
        },
        { signal },
      ),
    enabled: Boolean(namespaceName),
    placeholderData: keepPrevious,
    refetchInterval: dialog ? false : LIVE_REFETCH_MS,
  });

  const open = (next: DialogState) => {
    setDialogSeq((n) => n + 1);
    setDialog(next);
  };
  const closeKind = (kind: DialogState['kind']) => setDialog((d) => (d?.kind === kind ? null : d));

  const onApplied = (operation: IdentityOperation, result: BulkResult | undefined, clear: boolean) => {
    const label = t(`operations.${operation}`);
    notifyBulkResult(t, label, result);
    invalidate();
    if (clear) clearSelection();
    setDialog({ kind: 'result', title: t('result.title', { operation: label }), result });
  };

  const sorting = sortingFromParams(params);
  const onSortingChange = (updater: Updater<SortingState>) => {
    const next = typeof updater === 'function' ? updater(sorting) : updater;
    setParams({ ...params, ...sortParamsFromSorting(next) });
  };

  if (!namespaceName) return <EmptyState icon={FingerprintIcon} title={t('noNamespace')} />;

  const filtered = countActiveFilters(params) > 0;

  return (
    <>
      <PageHeader
        title={t('title')}
        description={t('description')}
        actions={
          <>
            <PermissionButton
              permission={PERMISSIONS.identityOperate}
              variant="outline"
              size="sm"
              onClick={() => open({ kind: 'revert' })}
            >
              <Undo2Icon />
              {t('revert.open')}
            </PermissionButton>
            <OperationsMenu
              operations={IDENTITY_OPERATIONS}
              onSelect={(operation) => open({ kind: 'bulk', operation })}
              label={t('bulk.menu')}
              heading={t('bulk.menuHeading')}
              site={params.site || undefined}
              icon={<LayersIcon />}
            />
            <PermissionButton
              permission={PERMISSIONS.identityWrite}
              site={params.site || undefined}
              size="sm"
              onClick={() => open({ kind: 'import' })}
            >
              <UploadIcon />
              {t('import.open')}
            </PermissionButton>
            <PermissionButton
              permission={PERMISSIONS.identityWrite}
              site={params.site || undefined}
              size="sm"
              onClick={() => open({ kind: 'new' })}
            >
              <PlusIcon />
              {t('newIdentity.open')}
            </PermissionButton>
          </>
        }
      />
      <PageIntro
        page="identities"
        links={[
          { to: '/identity-types', labelKey: 'nav.identityTypes' },
          { to: '/accounts', labelKey: 'nav.accounts' },
        ]}
      />
      <DataTable<Identity>
        columns={columns}
        data={query.data?.identities}
        getRowId={(row) => row.id}
        isLoading={query.isLoading}
        isFetching={query.isFetching}
        error={query.error ?? undefined}
        onRetry={() => void query.refetch()}
        emptyTitle={filtered ? t('empty.filteredTitle') : t('empty.title')}
        emptyDescription={filtered ? t('empty.filteredDescription') : t('empty.description')}
        sorting={sorting}
        onSortingChange={onSortingChange}
        manualSorting
        enableRowSelection
        rowSelection={rowSelection}
        onRowSelectionChange={onRowSelectionChange}
        bulkActions={(rows) => (
          <SelectionActions
            rows={rows}
            onOperate={(operation, selected) => {
              const sites = new Set(selected.map((r) => r.site));
              open({
                kind: 'operate',
                operation,
                ids: selected.map((r) => r.id),
                site: sites.size === 1 ? [...sites][0] : undefined,
              });
            }}
          />
        )}
        onRowClick={(row) => void navigate({ to: '/identities/$id', params: { id: row.id } })}
        virtualize="auto"
        maxHeight="calc(100vh - 17rem)"
        pagination={{ pager, nextPageToken: query.data?.nextPageToken, total: query.data?.total }}
        toolbar={
          <IdentityFilterBar
            params={params}
            onChange={setParams}
            onRefresh={() => void query.refetch()}
            isFetching={query.isFetching}
          />
        }
      />

      {dialog?.kind === 'operate' && (
        <OperationDialog
          key={`operate-${dialogSeq}`}
          open
          onOpenChange={(next) => !next && closeKind('operate')}
          operation={dialog.operation}
          ids={dialog.ids}
          site={dialog.site}
          onApplied={(result) => onApplied(dialog.operation, result, true)}
        />
      )}
      {dialog?.kind === 'bulk' && (
        <BulkFilterOperationDialog
          key={`bulk-${dialogSeq}`}
          open
          onOpenChange={(next) => !next && closeKind('bulk')}
          operation={dialog.operation}
          params={params}
          onApplied={(result) => onApplied(dialog.operation, result, false)}
        />
      )}
      {dialog?.kind === 'import' && (
        <ImportDialog
          key={`import-${dialogSeq}`}
          open
          onOpenChange={(next) => !next && closeKind('import')}
          defaultSite={params.site}
          defaultType={params.type}
        />
      )}
      {dialog?.kind === 'new' && (
        <NewIdentityDialog
          key={`new-${dialogSeq}`}
          open
          onOpenChange={(next) => !next && closeKind('new')}
          defaultSite={params.site}
          defaultType={params.type}
        />
      )}
      {dialog?.kind === 'revert' && (
        <RevertActionsDialog
          key={`revert-${dialogSeq}`}
          open
          onOpenChange={(next) => !next && closeKind('revert')}
          defaultSite={params.site}
        />
      )}
      {dialog?.kind === 'result' && (
        <BulkResultDialog
          open
          onOpenChange={(next) => !next && closeKind('result')}
          title={dialog.title}
          result={dialog.result}
        />
      )}
    </>
  );
}

/** Identities: filterable live table with bulk operations, import and action revert. */
export default function IdentitiesPage() {
  return (
    <RequirePermission permission={PERMISSIONS.identityRead}>
      <IdentitiesContent />
    </RequirePermission>
  );
}
