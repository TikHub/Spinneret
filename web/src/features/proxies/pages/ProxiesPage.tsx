import { useNavigate, useSearch } from '@tanstack/react-router';
import { UploadIcon } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';

import { useAuth } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';
import { PermissionButton, RequirePermission } from '@/app/auth/PermissionGate';
import { EmptyState } from '@/components/EmptyState';
import { PageHeader } from '@/components/PageHeader';
import { PageIntro } from '@/components/PageIntro';
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs';

import { ImportProxiesDialog } from '../components/ImportProxiesDialog';
import { ProviderStatsTab } from '../components/ProviderStatsTab';
import { ProxiesTab } from '../components/ProxiesTab';

type ProxiesPageTab = 'proxies' | 'providers';

function ProxiesContent() {
  const { t } = useTranslation('proxies');
  const { namespaceName } = useAuth();
  const search = useSearch({ from: '/_app/proxies' });
  const navigate = useNavigate({ from: '/proxies' });
  const [importOpen, setImportOpen] = useState(false);
  const tab: ProxiesPageTab = search.tab === 'providers' ? 'providers' : 'proxies';

  if (!namespaceName) {
    return (
      <>
        <PageHeader title={t('title')} />
        <EmptyState title={t('noNamespace')} />
      </>
    );
  }

  const setTab = (next: string) =>
    void navigate({
      search: (prev) => ({ ...prev, tab: next === 'providers' ? 'providers' : undefined }),
      replace: true,
    });

  return (
    <>
      <PageHeader
        title={t('title')}
        description={t('description', { namespace: namespaceName })}
        actions={
          <PermissionButton permission={PERMISSIONS.proxyWrite} onClick={() => setImportOpen(true)}>
            <UploadIcon />
            {t('import.button')}
          </PermissionButton>
        }
      />
      <PageIntro page="proxies" links={[{ to: '/policies', labelKey: 'nav.policies' }]} />
      <Tabs value={tab} onValueChange={setTab}>
        <TabsList>
          <TabsTrigger value="proxies">{t('tabs.proxies')}</TabsTrigger>
          <TabsTrigger value="providers">{t('tabs.providers')}</TabsTrigger>
        </TabsList>
        <TabsContent value="proxies">
          <ProxiesTab paused={importOpen} />
        </TabsContent>
        <TabsContent value="providers">
          <ProviderStatsTab />
        </TabsContent>
      </Tabs>
      {importOpen && <ImportProxiesDialog open onOpenChange={setImportOpen} />}
    </>
  );
}

/** Proxy pool: health table with bulk operations, import and provider statistics. */
export default function ProxiesPage() {
  return (
    <RequirePermission permission={PERMISSIONS.proxyRead}>
      <ProxiesContent />
    </RequirePermission>
  );
}
