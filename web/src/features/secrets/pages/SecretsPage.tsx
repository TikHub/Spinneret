import { useQueryClient } from '@tanstack/react-query';
import { KeyRoundIcon, PlusIcon, RefreshCwIcon } from 'lucide-react';
import { useCallback, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import { useAuth } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';
import { PermissionButton, RequirePermission } from '@/app/auth/PermissionGate';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { DataTable, useCursorPagination } from '@/components/data-table';
import { EmptyState } from '@/components/EmptyState';
import { FilterBar } from '@/components/FilterBar';
import { PageHeader } from '@/components/PageHeader';
import { PageIntro } from '@/components/PageIntro';
import { TagsInput } from '@/components/TagsInput';
import { Button } from '@/components/ui/button';
import { Card } from '@/components/ui/card';
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs';
import { type SecretInfo } from '@/gen/spinneret/v1/secret_admin_pb';
import { secretAdminClient } from '@/lib/clients';

import { KekPanel } from '../components/KekPanel';
import { RevealedSecretDialog } from '../components/RevealedSecretDialog';
import { RevealSecretDialog } from '../components/RevealSecretDialog';
import { SecretDetailSheet } from '../components/SecretDetailSheet';
import { SecretFolderTree } from '../components/SecretFolderTree';
import { SecretFormDialog } from '../components/SecretFormDialog';
import { useSecretColumns } from '../components/useSecretColumns';
import { buildSecretFolders, inFolder, isValidSecretTag } from '../secretPath';
import { useRevealTimer } from '../useRevealTimer';
import { useSecretPaths, useSecrets } from '../useSecretsApi';

/** Form dialog state; the edited secret is kept while the dialog animates closed. */
interface FormState {
  open: boolean;
  secret?: SecretInfo;
}

function SecretsList() {
  const { t } = useTranslation('secrets');
  const { tenantId, namespaceName } = useAuth();
  const queryClient = useQueryClient();
  const [folder, setFolder] = useState('');
  const [search, setSearch] = useState('');
  const [tags, setTags] = useState<string[]>([]);
  const [selected, setSelected] = useState<SecretInfo>();
  const [form, setForm] = useState<FormState>({ open: false });
  const [revealTarget, setRevealTarget] = useState<SecretInfo>();
  const [revealedPath, setRevealedPath] = useState('');
  const [deleteTarget, setDeleteTarget] = useState<SecretInfo>();
  const timer = useRevealTimer();

  // The API has no prefix filter: narrow with a substring search, then keep exact prefix matches.
  const filters = useMemo(() => ({ search: search || folder, tags }), [search, folder, tags]);
  const pager = useCursorPagination({ resetOn: [tenantId, namespaceName, filters, folder] });
  const secrets = useSecrets(filters, pager);
  const paths = useSecretPaths();
  const folders = useMemo(() => buildSecretFolders(paths.data?.paths ?? []), [paths.data]);
  const rows = useMemo(
    () => secrets.data?.secrets.filter((s) => inFolder(s.path, folder)),
    [secrets.data, folder],
  );

  const openReveal = useCallback((secret: SecretInfo) => setRevealTarget(secret), []);
  const openEdit = useCallback((secret: SecretInfo) => setForm({ open: true, secret }), []);
  const openDelete = useCallback((secret: SecretInfo) => setDeleteTarget(secret), []);
  const columns = useSecretColumns({ onReveal: openReveal, onEdit: openEdit, onDelete: openDelete });

  const deleteSecret = async () => {
    if (!deleteTarget) return;
    await secretAdminClient.deleteSecret({ id: deleteTarget.id });
    toast.success(t('delete.success', { path: deleteTarget.path }));
    if (selected?.id === deleteTarget.id) setSelected(undefined);
    await queryClient.invalidateQueries({ queryKey: ['secrets'] });
  };

  const activeFilters = (search ? 1 : 0) + (tags.length > 0 ? 1 : 0) + (folder ? 1 : 0);

  return (
    <div className="grid items-start gap-4 lg:grid-cols-[16rem_minmax(0,1fr)]">
      <Card className="overflow-hidden lg:sticky lg:top-4">
        <div className="border-b px-3 py-2 text-xs font-medium text-muted-foreground">
          {t('folders.title')}
        </div>
        <div className="max-h-[calc(100vh-16rem)] overflow-y-auto">
          <SecretFolderTree
            folders={folders}
            total={paths.data?.paths.length ?? 0}
            selected={folder}
            onSelect={setFolder}
            isLoading={paths.isLoading}
            error={paths.error}
            onRetry={() => void paths.refetch()}
            truncated={paths.data?.truncated ?? false}
          />
        </div>
      </Card>
      <DataTable
        columns={columns}
        data={rows}
        getRowId={(s) => s.id}
        isLoading={secrets.isLoading}
        isFetching={secrets.isFetching}
        error={secrets.error}
        onRetry={() => void secrets.refetch()}
        onRowClick={setSelected}
        emptyTitle={activeFilters > 0 ? t('list.noMatches') : t('list.empty')}
        emptyDescription={activeFilters > 0 ? undefined : t('list.emptyDescription')}
        emptyAction={
          activeFilters === 0 ? (
            <PermissionButton
              permission={PERMISSIONS.secretWrite}
              size="sm"
              onClick={() => setForm({ open: true })}
            >
              <PlusIcon />
              {t('actions.create')}
            </PermissionButton>
          ) : undefined
        }
        initialColumnVisibility={{ updatedAt: false }}
        toolbar={
          <FilterBar
            search={{ value: search, onChange: setSearch, placeholder: t('list.search') }}
            activeCount={activeFilters}
            onReset={() => {
              setSearch('');
              setTags([]);
              setFolder('');
            }}
            actions={
              <>
                <Button
                  variant="outline"
                  size="icon-sm"
                  aria-label={t('common:actions.refresh')}
                  onClick={() => {
                    void secrets.refetch();
                    void paths.refetch();
                  }}
                >
                  <RefreshCwIcon className={secrets.isFetching ? 'animate-spin' : undefined} />
                </Button>
                <PermissionButton
                  permission={PERMISSIONS.secretWrite}
                  size="sm"
                  onClick={() => setForm({ open: true })}
                >
                  <PlusIcon />
                  {t('actions.create')}
                </PermissionButton>
              </>
            }
          >
            <div className="w-64">
              <TagsInput
                value={tags}
                onChange={setTags}
                placeholder={t('list.tagsFilter')}
                validate={isValidSecretTag}
                maxTags={32}
              />
            </div>
          </FilterBar>
        }
        pagination={{ pager, nextPageToken: secrets.data?.nextPageToken, total: secrets.data?.total }}
      />

      <SecretDetailSheet
        secret={selected}
        onOpenChange={(open) => !open && setSelected(undefined)}
        onReveal={openReveal}
        onEdit={openEdit}
        onDelete={openDelete}
      />
      <SecretFormDialog
        open={form.open}
        onOpenChange={(open) => !open && setForm((prev) => ({ ...prev, open: false }))}
        secret={form.secret}
        pathPrefix={folder}
      />
      <RevealSecretDialog
        secret={revealTarget}
        onOpenChange={(open) => !open && setRevealTarget(undefined)}
        onRevealed={(value) => {
          setRevealedPath(revealTarget?.path ?? '');
          timer.show(value);
          void queryClient.invalidateQueries({ queryKey: ['secrets'] });
        }}
      />
      <RevealedSecretDialog path={revealedPath} timer={timer} />
      <ConfirmDialog
        open={deleteTarget !== undefined}
        onOpenChange={(open) => !open && setDeleteTarget(undefined)}
        title={t('delete.title')}
        description={t('delete.description', { path: deleteTarget?.path ?? '' })}
        confirmLabel={t('common:actions.delete')}
        destructive
        confirmText={deleteTarget?.path}
        onConfirm={deleteSecret}
      />
    </div>
  );
}

function SecretsContent() {
  const { t } = useTranslation('secrets');
  const { tenantId, namespaceName, isPlatformAdmin } = useAuth();
  const [tab, setTab] = useState('secrets');
  // Folder, filters, selection and dialogs belong to one namespace: start over after a switch.
  const list = <SecretsList key={`${tenantId ?? ''}/${namespaceName ?? ''}`} />;

  if (!namespaceName) return <EmptyState title={t('noNamespace')} />;

  return (
    <>
      <PageHeader title={t('title')} description={t('description', { namespace: namespaceName })} />
      <PageIntro
        page="secrets"
        links={[
          { to: '/config', labelKey: 'nav.config' },
          { to: '/access/tokens', labelKey: 'nav.tokens' },
        ]}
      />
      {isPlatformAdmin ? (
        <Tabs value={tab} onValueChange={setTab}>
          <TabsList>
            <TabsTrigger value="secrets">{t('tabs.secrets')}</TabsTrigger>
            <TabsTrigger value="kek">
              <KeyRoundIcon />
              {t('tabs.kek')}
            </TabsTrigger>
          </TabsList>
          <TabsContent value="secrets">{list}</TabsContent>
          <TabsContent value="kek">
            <KekPanel />
          </TabsContent>
        </Tabs>
      ) : (
        list
      )}
    </>
  );
}

/** Vault secrets: folder tree, masked values, versions, audited reveal, access logs and KEK rewrap. */
export default function SecretsPage() {
  return (
    <RequirePermission permission={PERMISSIONS.secretList}>
      <SecretsContent />
    </RequirePermission>
  );
}
