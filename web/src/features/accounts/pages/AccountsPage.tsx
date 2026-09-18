import { useQuery } from '@tanstack/react-query';
import { useNavigate, useSearch } from '@tanstack/react-router';
import { PlusIcon, RefreshCwIcon, UsersIcon } from 'lucide-react';
import { useCallback, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import { useAuth, useScopedPlaceholder, useScopedQueryKey } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';
import { PermissionButton, RequirePermission } from '@/app/auth/PermissionGate';
import { LIVE_REFETCH_MS } from '@/app/queryClient';
import { DataTable, useCursorPagination } from '@/components/data-table';
import { EmptyState } from '@/components/EmptyState';
import { FilterBar } from '@/components/FilterBar';
import { PageHeader } from '@/components/PageHeader';
import { PageIntro } from '@/components/PageIntro';
import { Button } from '@/components/ui/button';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { BulkResultDialog } from '@/features/identities/components/BulkResultDialog';
import { ALL_VALUE, SiteSelect } from '@/features/identities/components/SiteSelect';
import { useInvalidateIdentityData } from '@/features/identities/notify';
import { summarizeBulkResult } from '@/features/identities/operations';
import { type BulkResult } from '@/gen/spinneret/v1/common_pb';
import { type Account } from '@/gen/spinneret/v1/identity_admin_pb';
import { identityClient } from '@/lib/clients';

import {
  ACCOUNT_STATES,
  mergeAccountSearch,
  parseAccountSearch,
  type AccountListParams,
  type AccountOperation,
  type AccountState,
} from '../accounts';
import { AccountOperationDialog } from '../components/AccountOperationDialog';
import { AccountUpsertDialog } from '../components/AccountUpsertDialog';
import { useAccountColumns } from '../components/useAccountColumns';

type DialogState =
  | { kind: 'upsert'; account?: Account; seq: number }
  | { kind: 'operate'; operation: AccountOperation; account: Account; seq: number }
  | { kind: 'result'; title: string; result: BulkResult | undefined; seq: number };

function AccountsContent() {
  const { t } = useTranslation(['accounts', 'identities']);
  const { namespaceName, tenantId } = useAuth();
  const search = useSearch({ from: '/_app/accounts' });
  const navigate = useNavigate({ from: '/accounts' });
  const key = useScopedQueryKey();
  const keepPrevious = useScopedPlaceholder();
  const invalidate = useInvalidateIdentityData();
  const params = useMemo(() => parseAccountSearch(search), [search]);
  const [dialog, setDialog] = useState<DialogState | null>(null);

  const setParams = useCallback(
    (patch: Partial<AccountListParams>) => {
      void navigate({
        search: (prev) => mergeAccountSearch(prev, { ...parseAccountSearch(prev), ...patch }),
        replace: true,
      });
    },
    [navigate],
  );

  const pager = useCursorPagination({ resetOn: [tenantId, namespaceName, params] });
  const query = useQuery({
    queryKey: key('accounts', 'list', params, pager.pageToken, pager.pageSize),
    queryFn: ({ signal }) =>
      identityClient.listAccounts(
        {
          namespace: namespaceName ?? '',
          site: params.site,
          state: params.state,
          search: params.search,
          pageSize: pager.pageSize,
          pageToken: pager.pageToken,
        },
        { signal },
      ),
    enabled: Boolean(namespaceName),
    placeholderData: keepPrevious,
    refetchInterval: dialog ? false : LIVE_REFETCH_MS,
  });

  const onOperate = useCallback(
    (operation: AccountOperation, account: Account) =>
      setDialog((d) => ({ kind: 'operate', operation, account, seq: (d?.seq ?? 0) + 1 })),
    [setDialog],
  );
  const onEdit = useCallback(
    (account: Account) => setDialog((d) => ({ kind: 'upsert', account, seq: (d?.seq ?? 0) + 1 })),
    [setDialog],
  );
  const columns = useAccountColumns({ onOperate, onEdit });
  const closeKind = (kind: DialogState['kind']) => setDialog((d) => (d?.kind === kind ? null : d));

  if (!namespaceName) return <EmptyState icon={UsersIcon} title={t('noNamespace')} />;

  const activeCount = (params.site ? 1 : 0) + (params.state ? 1 : 0) + (params.search ? 1 : 0);

  return (
    <>
      <PageHeader
        title={t('title')}
        description={t('description')}
        actions={
          <PermissionButton
            permission={PERMISSIONS.identityWrite}
            site={params.site || undefined}
            size="sm"
            onClick={() => setDialog((d) => ({ kind: 'upsert', seq: (d?.seq ?? 0) + 1 }))}
          >
            <PlusIcon />
            {t('actions.upsert')}
          </PermissionButton>
        }
      />
      <PageIntro page="accounts" links={[{ to: '/identities', labelKey: 'nav.identities' }]} />
      <DataTable<Account>
        columns={columns}
        data={query.data?.accounts}
        getRowId={(row) => row.id}
        isLoading={query.isLoading}
        isFetching={query.isFetching}
        error={query.error ?? undefined}
        onRetry={() => void query.refetch()}
        emptyTitle={activeCount > 0 ? t('empty.filteredTitle') : t('empty.title')}
        emptyDescription={activeCount > 0 ? t('empty.filteredDescription') : t('empty.description')}
        pagination={{ pager, nextPageToken: query.data?.nextPageToken, total: query.data?.total }}
        toolbar={
          <FilterBar
            search={{
              value: params.search,
              onChange: (value) => setParams({ search: value }),
              placeholder: t('filters.search'),
            }}
            activeCount={activeCount}
            onReset={() => setParams({ site: '', state: '', search: '' })}
            actions={
              <Button
                variant="outline"
                size="icon-sm"
                onClick={() => void query.refetch()}
                aria-label={t('common:actions.refresh')}
              >
                <RefreshCwIcon className={query.isFetching ? 'animate-spin' : undefined} />
              </Button>
            }
          >
            <SiteSelect size="sm" allowAll value={params.site} onChange={(site) => setParams({ site })} />
            <Select
              value={params.state || ALL_VALUE}
              onValueChange={(v) => setParams({ state: v === ALL_VALUE ? '' : (v as AccountState) })}
            >
              <SelectTrigger size="sm" className="w-36" aria-label={t('filters.state')}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={ALL_VALUE}>{t('filters.allStates')}</SelectItem>
                {ACCOUNT_STATES.map((state) => (
                  <SelectItem key={state} value={state}>
                    {t(`common:states.${state}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </FilterBar>
        }
      />
      {dialog?.kind === 'upsert' && (
        <AccountUpsertDialog
          key={`upsert-${dialog.seq}`}
          open
          onOpenChange={(next) => !next && closeKind('upsert')}
          account={dialog.account}
          defaultSite={params.site}
        />
      )}
      {dialog?.kind === 'operate' && (
        <AccountOperationDialog
          key={`operate-${dialog.seq}`}
          open
          onOpenChange={(next) => !next && closeKind('operate')}
          account={dialog.account}
          operation={dialog.operation}
          onApplied={(response) => {
            const label = t(`operations.${dialog.operation}`);
            const ref = response.account?.externalRef ?? dialog.account.externalRef;
            const summary = summarizeBulkResult(response.identities);
            toast.success(t('operation.toastApplied', { operation: label, ref, count: summary.succeeded }));
            if (summary.failed > 0) {
              toast.warning(t('operation.toastIdentityFailures', { count: summary.failed }));
            }
            invalidate();
            if (summary.matched > 0) {
              setDialog({
                kind: 'result',
                title: t('result.title', { operation: label, ref }),
                result: response.identities,
                seq: dialog.seq + 1,
              });
            }
          }}
        />
      )}
      {dialog?.kind === 'result' && (
        <BulkResultDialog
          open
          onOpenChange={(next) => !next && closeKind('result')}
          title={dialog.title}
          description={t('result.description')}
          result={dialog.result}
        />
      )}
    </>
  );
}

/** Accounts: identities grouped by user account with ban, cooldown and disable operations. */
export default function AccountsPage() {
  return (
    <RequirePermission permission={PERMISSIONS.identityRead}>
      <AccountsContent />
    </RequirePermission>
  );
}
