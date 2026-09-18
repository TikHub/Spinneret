import { useState } from 'react';
import { useTranslation } from 'react-i18next';

import { PERMISSIONS } from '@/app/auth/permissions';
import { RequirePermission } from '@/app/auth/PermissionGate';
import { PageHeader } from '@/components/PageHeader';
import { PageIntro } from '@/components/PageIntro';
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs';

import { AlertHistoryTab } from '../components/AlertHistoryTab';
import { ChannelsTab } from '../components/ChannelsTab';

function NotificationsContent() {
  const { t } = useTranslation('notifications');
  const [tab, setTab] = useState('channels');
  return (
    <>
      <PageHeader title={t('title')} description={t('description')} />
      <PageIntro page="notifications" links={[{ to: '/breakers', labelKey: 'nav.breakers' }]} />
      <Tabs value={tab} onValueChange={setTab}>
        <TabsList>
          <TabsTrigger value="channels">{t('tabs.channels')}</TabsTrigger>
          <TabsTrigger value="alerts">{t('tabs.alerts')}</TabsTrigger>
        </TabsList>
        <TabsContent value="channels">
          <ChannelsTab />
        </TabsContent>
        <TabsContent value="alerts">
          <AlertHistoryTab />
        </TabsContent>
      </Tabs>
    </>
  );
}

/** Notification channels (CRUD, enable, test) and the alert history with delivery results. */
export default function NotificationsPage() {
  return (
    <RequirePermission permission={PERMISSIONS.notifyRead}>
      <NotificationsContent />
    </RequirePermission>
  );
}
