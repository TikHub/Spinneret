import { useNavigate, useSearch } from '@tanstack/react-router';
import { type RowSelectionState } from '@tanstack/react-table';
import { useCallback, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import { useAuth } from '@/app/auth/AuthContext';
import { DataTable, useCursorPagination } from '@/components/data-table';
import { toResetKey } from '@/components/data-table/pagination';
import { type BulkResult } from '@/gen/spinneret/v1/common_pb';
import { type Proxy } from '@/gen/spinneret/v1/proxy_admin_pb';

import {
  countActiveFilters,
  parseProxyFilters,
  proxyFiltersToSearch,
  type ProxyFilters,
} from '../proxyFilters';
import {
  summarizeBulkResult,
  type BulkSummary,
  type ProxyBulkAction,
  type ProxyOperation,
} from '../proxyOperations';
import { useCheckAction } from '../useCheckAction';
import { useProxyList } from '../useProxies';
import { BulkResultDialog } from './BulkResultDialog';
import { DeleteProxiesDialog } from './DeleteProxiesDialog';
import { EditProxyDialog } from './EditProxyDialog';
import { OperateProxiesDialog } from './OperateProxiesDialog';
import { type ProxyAction } from './ProxyActionsMenu';
import { ProxyBulkActions } from './ProxyBulkActions';
import { ProxyDetailSheet } from './ProxyDetailSheet';
import { ProxyFilterBar } from './ProxyFilterBar';
import { useProxyColumns } from './useProxyColumns';

type DialogState =
  | { type: 'operate'; operation: ProxyOperation; ids: string[]; subject?: string }
  | { type: 'delete'; ids: string[]; boundIdentities: number; subject?: string }
  | { type: 'edit'; proxy: Proxy }
  | { type: 'result'; title: string; summary: BulkSummary; result: BulkResult };

const INITIAL_HIDDEN_COLUMNS = { id: false };

export interface ProxiesTabProps {
  /** Pause auto refresh (e.g. the import dialog is open). */
  paused: boolean;
}

/** Proxies table with URL-synced filters, bulk operations, row actions and the detail sheet. */
export function ProxiesTab({ paused }: ProxiesTabProps) {
  const { t } = useTranslation('proxies');
  const { tenantId, namespaceName } = useAuth();
  const search = useSearch({ from: '/_app/proxies' });
  const navigate = useNavigate({ from: '/proxies' });
  const filters = useMemo(() => parseProxyFilters(search), [search]);

  const [dialog, setDialog] = useState<DialogState | null>(null);
  const [detail, setDetail] = useState<Proxy>();
  const pager = useCursorPagination({ resetOn: [tenantId, namespaceName, filters] });
  const query = useProxyList(filters, pager, paused || dialog !== null || detail !== undefined);
  const { checkingIds, check } = useCheckAction();

  // Selection belongs to one scope, filter set and page; it resets when any of them changes.
  const selectionKey = toResetKey([tenantId, namespaceName, filters, pager.pageToken, pager.pageSize]);
  const [selection, setSelection] = useState<{ key: string; rows: RowSelectionState }>({
    key: selectionKey,
    rows: {},
  });
  const rowSelection = selection.key === selectionKey ? selection.rows : {};
  const clearSelection = () => setSelection({ key: selectionKey, rows: {} });

  const setFilters = (next: ProxyFilters) => {
    void navigate({ search: (prev) => ({ ...prev, ...proxyFiltersToSearch(next) }), replace: true });
  };

  const proxies = query.data?.proxies;
  const onAction = useCallback(
    (action: ProxyAction, proxy: Proxy) => {
      const subject = proxy.displayUrl || proxy.id;
      if (action === 'details') setDetail(proxy);
      else if (action === 'check') void check(proxy);
      else if (action === 'edit') setDialog({ type: 'edit', proxy });
      else if (action === 'delete') {
        setDialog({ type: 'delete', ids: [proxy.id], boundIdentities: proxy.boundIdentities, subject });
      } else setDialog({ type: 'operate', operation: action, ids: [proxy.id], subject });
    },
    [check],
  );
  const columns = useProxyColumns({ onAction, checkingIds });

  const onBulkAction = (action: ProxyBulkAction, rows: readonly Proxy[]) => {
    const ids = rows.map((p) => p.id);
    if (action === 'delete') {
      const boundIdentities = rows.reduce((sum, p) => sum + p.boundIdentities, 0);
      setDialog({ type: 'delete', ids, boundIdentities });
    } else {
      setDialog({ type: 'operate', operation: action, ids });
    }
  };

  const closeDialog = (type: DialogState['type']) =>
    setDialog((current) => (current?.type === type ? null : current));

  const reportBulk = (
    label: string,
    requested: number,
    result: BulkResult | undefined,
    doneMessage: (count: number) => string,
  ) => {
    const summary = summarizeBulkResult(result, requested);
    clearSelection();
    if (summary.failed === 0) {
      toast.success(doneMessage(summary.succeeded));
      return;
    }
    toast.warning(
      t('bulk.partial', { operation: label, succeeded: summary.succeeded, failed: summary.failed }),
    );
    if (result) {
      setDialog({ type: 'result', title: t('bulk.resultTitle', { operation: label }), summary, result });
    }
  };

  const filtered = countActiveFilters(filters) > 0;
  const checkingDetail = detail ? checkingIds.has(detail.id) : false;

  return (
    <>
      <DataTable
        columns={columns}
        data={proxies}
        getRowId={(p) => p.id}
        isLoading={query.isLoading}
        isFetching={query.isFetching}
        error={query.error}
        onRetry={() => void query.refetch()}
        emptyTitle={filtered ? t('empty.filteredTitle') : t('empty.title')}
        emptyDescription={filtered ? t('empty.filteredDescription') : t('empty.description')}
        initialColumnVisibility={INITIAL_HIDDEN_COLUMNS}
        enableRowSelection
        rowSelection={rowSelection}
        onRowSelectionChange={(updater) =>
          setSelection((prev) => {
            const base = prev.key === selectionKey ? prev.rows : {};
            return { key: selectionKey, rows: typeof updater === 'function' ? updater(base) : updater };
          })
        }
        bulkActions={(rows) => <ProxyBulkActions onAction={(action) => onBulkAction(action, rows)} />}
        onRowClick={(proxy) => setDetail(proxy)}
        pagination={{ pager, nextPageToken: query.data?.nextPageToken, total: query.data?.total }}
        toolbar={
          <ProxyFilterBar
            filters={filters}
            onChange={setFilters}
            onRefresh={() => void query.refetch()}
            refreshing={query.isFetching}
          />
        }
      />

      <ProxyDetailSheet
        proxyId={detail?.id}
        initial={detail}
        onOpenChange={(open) => !open && setDetail(undefined)}
        onAction={onAction}
        checking={checkingDetail}
        paused={dialog !== null}
      />

      {dialog?.type === 'operate' && (
        <OperateProxiesDialog
          open
          onOpenChange={(open) => !open && closeDialog('operate')}
          operation={dialog.operation}
          ids={dialog.ids}
          subject={dialog.subject}
          onDone={(operation, ids, response) =>
            reportBulk(t(`operations.${operation}`), ids.length, response.result, (count) =>
              t('bulk.done', { operation: t(`operations.${operation}`), count }),
            )
          }
        />
      )}
      {dialog?.type === 'delete' && (
        <DeleteProxiesDialog
          open
          onOpenChange={(open) => !open && closeDialog('delete')}
          ids={dialog.ids}
          boundIdentities={dialog.boundIdentities}
          subject={dialog.subject}
          onDone={(ids, response) => {
            if (
              detail &&
              ids.includes(detail.id) &&
              !response.result?.failed.some((f) => f.id === detail.id)
            ) {
              setDetail(undefined);
            }
            reportBulk(t('operations.delete'), ids.length, response.result, (count) =>
              t('delete.done', { count }),
            );
          }}
        />
      )}
      {dialog?.type === 'edit' && (
        <EditProxyDialog proxy={dialog.proxy} open onOpenChange={(open) => !open && closeDialog('edit')} />
      )}
      {dialog?.type === 'result' && (
        <BulkResultDialog
          open
          onOpenChange={(open) => !open && closeDialog('result')}
          title={dialog.title}
          summary={dialog.summary}
          failures={dialog.result.failed}
        />
      )}
    </>
  );
}
