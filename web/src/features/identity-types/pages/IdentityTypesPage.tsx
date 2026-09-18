import { useQuery } from '@tanstack/react-query';
import { useNavigate, useSearch } from '@tanstack/react-router';
import { FileCode2Icon, PlusIcon, RefreshCwIcon } from 'lucide-react';
import { useCallback, useState } from 'react';
import { useTranslation } from 'react-i18next';

import { useAuth, useScopedPlaceholder, useScopedQueryKey } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';
import { PermissionButton, RequirePermission } from '@/app/auth/PermissionGate';
import { DataTable, useCursorPagination } from '@/components/data-table';
import { EmptyState } from '@/components/EmptyState';
import { FilterBar } from '@/components/FilterBar';
import { PageHeader } from '@/components/PageHeader';
import { PageIntro } from '@/components/PageIntro';
import { Button } from '@/components/ui/button';
import { SiteSelect } from '@/features/identities/components/SiteSelect';
import { type IdentityType } from '@/gen/spinneret/v1/identity_admin_pb';
import { identityClient } from '@/lib/clients';

import { DeleteIdentityTypeDialog } from '../components/DeleteIdentityTypeDialog';
import { IdentityTypeEditorDialog, type EditorTab } from '../components/IdentityTypeEditorDialog';
import { useIdentityTypeColumns, type IdentityTypeAction } from '../components/useIdentityTypeColumns';

type DialogState =
  | { kind: 'editor'; type?: IdentityType; tab: EditorTab; seq: number }
  | { kind: 'delete'; type: IdentityType; seq: number };

function readText(value: unknown, max: number): string {
  return typeof value === 'string'
    ? value.trim().slice(0, max)
    : typeof value === 'number'
      ? String(value)
      : '';
}

function IdentityTypesContent() {
  const { t } = useTranslation(['identity-types', 'identities']);
  const { namespaceName, tenantId, can } = useAuth();
  const search = useSearch({ from: '/_app/identity-types' });
  const navigate = useNavigate({ from: '/identity-types' });
  const key = useScopedQueryKey();
  const keepPrevious = useScopedPlaceholder();
  const site = readText(search.site, 64);
  const text = readText(search.search, 256);
  const [dialog, setDialog] = useState<DialogState | null>(null);

  const setSearch = useCallback(
    (patch: { site?: string; search?: string }) => {
      void navigate({
        search: (prev) => {
          const next = { ...prev, ...patch };
          return {
            ...next,
            site: next.site || undefined,
            search: next.search || undefined,
          };
        },
        replace: true,
      });
    },
    [navigate],
  );

  const pager = useCursorPagination({ resetOn: [tenantId, namespaceName, site, text] });
  const query = useQuery({
    queryKey: key('identity-types', 'list', site, text, pager.pageToken, pager.pageSize),
    queryFn: ({ signal }) =>
      identityClient.listIdentityTypes(
        {
          namespace: namespaceName ?? '',
          site,
          search: text,
          pageSize: pager.pageSize,
          pageToken: pager.pageToken,
        },
        { signal },
      ),
    enabled: Boolean(namespaceName),
    placeholderData: keepPrevious,
  });

  const nextSeq = (dialog?.seq ?? 0) + 1;
  const onAction = useCallback(
    (action: IdentityTypeAction, type: IdentityType) => {
      setDialog((current) => {
        const seq = (current?.seq ?? 0) + 1;
        return action === 'delete'
          ? { kind: 'delete', type, seq }
          : { kind: 'editor', type, tab: action === 'preview' ? 'preview' : 'spec', seq };
      });
    },
    [setDialog],
  );
  const canWrite = useCallback((typeSite: string) => can(PERMISSIONS.siteWrite, typeSite), [can]);
  const columns = useIdentityTypeColumns({ onAction, canWrite });
  const close = () => setDialog(null);
  const filtered = site !== '' || text !== '';

  const toolbar = (
    <FilterBar
      search={{
        value: text,
        onChange: (value) => setSearch({ search: value }),
        placeholder: t('filters.search'),
      }}
      activeCount={(site ? 1 : 0) + (text ? 1 : 0)}
      onReset={() => setSearch({ site: '', search: '' })}
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
      <SiteSelect size="sm" allowAll value={site} onChange={(value) => setSearch({ site: value })} />
    </FilterBar>
  );

  if (!namespaceName) return <EmptyState icon={FileCode2Icon} title={t('noNamespace')} />;

  return (
    <>
      <PageHeader
        title={t('title')}
        description={t('description')}
        actions={
          <PermissionButton
            permission={PERMISSIONS.siteWrite}
            site={site || undefined}
            size="sm"
            onClick={() => setDialog({ kind: 'editor', tab: 'spec', seq: nextSeq })}
          >
            <PlusIcon />
            {t('actions.create')}
          </PermissionButton>
        }
      />
      <PageIntro
        page="identity-types"
        links={[
          { to: '/identities', labelKey: 'nav.identities' },
          { to: '/sites', labelKey: 'nav.sites' },
        ]}
      />
      <DataTable<IdentityType>
        columns={columns}
        data={query.data?.identityTypes}
        getRowId={(row) => row.id}
        isLoading={query.isLoading}
        isFetching={query.isFetching}
        error={query.error ?? undefined}
        onRetry={() => void query.refetch()}
        emptyTitle={filtered ? t('empty.filteredTitle') : t('empty.title')}
        emptyDescription={filtered ? t('empty.filteredDescription') : t('empty.description')}
        onRowClick={(row) => onAction('edit', row)}
        pagination={{ pager, nextPageToken: query.data?.nextPageToken, total: query.data?.total }}
        toolbar={toolbar}
      />
      {dialog?.kind === 'editor' && (
        <IdentityTypeEditorDialog
          key={`editor-${dialog.seq}`}
          open
          onOpenChange={(next) => !next && close()}
          identityType={dialog.type}
          defaultSite={site}
          initialTab={dialog.tab}
        />
      )}
      {dialog?.kind === 'delete' && (
        <DeleteIdentityTypeDialog
          key={`delete-${dialog.seq}`}
          identityType={dialog.type}
          open
          onOpenChange={(next) => !next && close()}
        />
      )}
    </>
  );
}

/** Identity types: field templates and delivery mapping per site, with YAML editor and delivery preview. */
export default function IdentityTypesPage() {
  return (
    <RequirePermission permission={PERMISSIONS.identityRead}>
      <IdentityTypesContent />
    </RequirePermission>
  );
}
