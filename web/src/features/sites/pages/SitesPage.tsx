import { useNavigate, useSearch } from '@tanstack/react-router';
import { GlobeIcon, PlusIcon } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';

import { useAuth } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';
import { RequirePermission } from '@/app/auth/PermissionGate';
import { useCursorPagination } from '@/components/data-table';
import { EmptyState } from '@/components/EmptyState';
import { ErrorState } from '@/components/ErrorState';
import { PageHeader } from '@/components/PageHeader';
import { PageIntro } from '@/components/PageIntro';
import { Button } from '@/components/ui/button';
import { Skeleton } from '@/components/ui/skeleton';
import { type Site } from '@/gen/spinneret/v1/site_admin_pb';
import { isNotFound } from '@/lib/errors';

import { DeleteSiteDialog } from '../components/DeleteSiteDialog';
import { GuardedButton } from '../components/GuardedButton';
import { SiteDetail } from '../components/SiteDetail';
import { SiteFormDialog } from '../components/SiteFormDialog';
import { SiteList } from '../components/SiteList';
import { searchName } from '../siteSearch';
import { useSitePermissions } from '../useSitePermissions';
import { useSite, useSiteList } from '../useSites';

/** Sites per list page (the master list is compact). */
const SITE_PAGE_SIZE = 100;

type SiteDialog = { type: 'create' } | { type: 'edit'; site: Site } | { type: 'delete'; site: Site };

function SitesContent() {
  const { t } = useTranslation('sites');
  const { tenantId, namespaceName } = useAuth();
  const { canManageSites } = useSitePermissions();
  const search = useSearch({ from: '/_app/sites' });
  const navigate = useNavigate({ from: '/sites' });
  const [dialog, setDialog] = useState<SiteDialog | null>(null);

  const pager = useCursorPagination({ pageSize: SITE_PAGE_SIZE, resetOn: [tenantId, namespaceName] });
  const list = useSiteList(pager, dialog !== null);
  const sites = list.data?.sites;
  const requested = searchName(search.site);
  const selectedName = requested ?? sites?.[0]?.name;
  const detail = useSite(selectedName, dialog !== null);
  const site = detail.data?.site ?? sites?.find((s) => s.name === selectedName);
  const client = searchName(search.client);

  const select = (name: string | undefined) =>
    void navigate({
      search: (prev) => ({ ...prev, site: name, client: undefined }),
      replace: name === undefined,
    });
  const setClient = (next: string) =>
    void navigate({ search: (prev) => ({ ...prev, client: next }), replace: true });
  const closeDialog = (type: SiteDialog['type']) =>
    setDialog((current) => (current?.type === type ? null : current));

  if (!namespaceName) {
    return (
      <>
        <PageHeader title={t('title')} />
        <EmptyState title={t('noNamespace')} />
      </>
    );
  }

  const createButton = (
    <GuardedButton
      allowed={canManageSites}
      permission={PERMISSIONS.siteWrite}
      onClick={() => setDialog({ type: 'create' })}
    >
      <PlusIcon />
      {t('list.create')}
    </GuardedButton>
  );

  return (
    <>
      <PageHeader
        title={t('title')}
        description={t('description', { namespace: namespaceName })}
        actions={createButton}
      />
      <PageIntro
        page="sites"
        links={[
          { to: '/policies', labelKey: 'nav.policies' },
          { to: '/breakers', labelKey: 'nav.breakers' },
        ]}
      />
      <div className="grid items-start gap-4 lg:grid-cols-[minmax(15rem,19rem)_minmax(0,1fr)]">
        <SiteList
          sites={sites}
          total={list.data?.total}
          selected={selectedName}
          onSelect={select}
          isLoading={list.isLoading}
          error={list.error}
          onRetry={() => void list.refetch()}
          pager={pager}
          nextPageToken={list.data?.nextPageToken}
        />
        <div className="min-w-0">
          {site ? (
            <SiteDetail
              key={site.id}
              site={site}
              client={client}
              onClientChange={setClient}
              onEdit={() => setDialog({ type: 'edit', site })}
              onDelete={() => setDialog({ type: 'delete', site })}
              paused={dialog !== null}
            />
          ) : detail.isError ? (
            <ErrorState
              error={detail.error}
              title={isNotFound(detail.error) ? t('detail.notFound', { name: selectedName }) : undefined}
              onRetry={isNotFound(detail.error) ? undefined : () => void detail.refetch()}
            />
          ) : list.isLoading || detail.isLoading ? (
            <Skeleton className="h-96 w-full rounded-lg" />
          ) : (
            <EmptyState
              icon={GlobeIcon}
              title={sites?.length === 0 ? t('list.empty') : t('detail.selectSite')}
              description={sites?.length === 0 ? t('list.emptyDescription') : undefined}
              action={sites?.length === 0 ? createButton : undefined}
            />
          )}
          {detail.isError && requested && isNotFound(detail.error) && (
            <div className="mt-2 flex justify-center">
              <Button variant="outline" size="sm" onClick={() => select(undefined)}>
                {t('detail.clearSelection')}
              </Button>
            </div>
          )}
        </div>
      </div>

      {dialog?.type === 'create' && (
        <SiteFormDialog
          open
          onOpenChange={(open) => !open && closeDialog('create')}
          onSaved={(created) => select(created.name)}
        />
      )}
      {dialog?.type === 'edit' && (
        <SiteFormDialog
          site={dialog.site}
          open
          onOpenChange={(open) => !open && closeDialog('edit')}
          onSaved={() => undefined}
        />
      )}
      {dialog?.type === 'delete' && (
        <DeleteSiteDialog
          site={dialog.site}
          open
          onOpenChange={(open) => !open && closeDialog('delete')}
          onDeleted={(deleted) => {
            if (deleted.name === selectedName) select(undefined);
          }}
        />
      )}
    </>
  );
}

/** Sites and endpoint groups: master list, site detail with URI rule editor and URI tester. */
export default function SitesPage() {
  return (
    <RequirePermission permission={PERMISSIONS.siteRead}>
      <SitesContent />
    </RequirePermission>
  );
}
