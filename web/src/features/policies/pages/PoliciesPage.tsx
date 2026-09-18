import { useNavigate, useSearch } from '@tanstack/react-router';
import { MousePointerClickIcon, PlusIcon } from 'lucide-react';
import { useCallback, useState } from 'react';
import { useTranslation } from 'react-i18next';

import { useAuth } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';
import { PermissionButton, RequirePermission } from '@/app/auth/PermissionGate';
import { EmptyState } from '@/components/EmptyState';
import { PageHeader } from '@/components/PageHeader';
import { PageIntro } from '@/components/PageIntro';
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs';

import { NamespaceBindingsView } from '../components/bindings/NamespaceBindingsView';
import { ResolvePanel } from '../components/bindings/ResolvePanel';
import { DebuggerPanel } from '../components/debugger/DebuggerPanel';
import { NewPolicyDialog } from '../components/NewPolicyDialog';
import { PolicyDetail } from '../components/PolicyDetail';
import { PolicyList } from '../components/PolicyList';
import { DETAIL_TABS, type DetailTab } from '../selectors';
import { useUnsavedPolicyDrafts } from '../useUnsavedPolicyDrafts';

const VIEWS = ['policies', 'bindings', 'resolve', 'debugger'] as const;
type View = (typeof VIEWS)[number];

function oneOf<T extends string>(values: readonly T[], value: unknown, fallback: T): T {
  return typeof value === 'string' && (values as readonly string[]).includes(value) ? (value as T) : fallback;
}

function PoliciesContent() {
  const { t } = useTranslation('policies');
  const { namespaceName, tenantId } = useAuth();
  const search = useSearch({ from: '/_app/policies' });
  const navigate = useNavigate({ from: '/policies' });
  const view = oneOf(VIEWS, search.view, 'policies');
  const tab = oneOf(DETAIL_TABS, search.tab, 'editor');
  const selectedId = typeof search.id === 'string' && search.id !== '' ? search.id : undefined;
  const [creating, setCreating] = useState(false);
  // Unsaved editor text per policy, kept while switching policies; cleared on scope changes.
  const unsaved = useUnsavedPolicyDrafts(`${tenantId}/${namespaceName}`);

  const setView = (next: View) =>
    void navigate({ search: (prev) => ({ ...prev, view: next === 'policies' ? undefined : next }) });
  const openPolicy = useCallback(
    (id: string | undefined, nextTab?: DetailTab) =>
      void navigate({
        search: (prev) => ({
          ...prev,
          view: undefined,
          id,
          tab: nextTab === 'editor' ? undefined : (nextTab ?? prev.tab),
        }),
      }),
    [navigate],
  );

  if (!namespaceName) {
    return <EmptyState title={t('noNamespace')} />;
  }

  return (
    <>
      <PageHeader
        title={t('title')}
        description={t('description', { namespace: namespaceName })}
        actions={
          <PermissionButton permission={PERMISSIONS.policyWrite} onClick={() => setCreating(true)}>
            <PlusIcon />
            {t('actions.new')}
          </PermissionButton>
        }
      />
      <PageIntro
        page="policies"
        links={[
          { to: '/sites', labelKey: 'nav.sites' },
          { to: '/breakers', labelKey: 'nav.breakers' },
        ]}
      />
      <Tabs value={view} onValueChange={(v) => setView(v as View)}>
        <TabsList>
          {VIEWS.map((v) => (
            <TabsTrigger key={v} value={v}>
              {t(`views.${v}`)}
            </TabsTrigger>
          ))}
        </TabsList>
        <TabsContent value="policies">
          <div className="grid items-start gap-4 lg:grid-cols-[17rem_minmax(0,1fr)] 2xl:grid-cols-[20rem_minmax(0,1fr)]">
            <div className="lg:sticky lg:top-4 lg:flex lg:max-h-[calc(100vh-10rem)] lg:flex-col">
              <PolicyList
                selectedId={selectedId}
                onSelect={(id) => openPolicy(id)}
                onCreate={() => setCreating(true)}
              />
            </div>
            {selectedId ? (
              <PolicyDetail
                key={selectedId}
                id={selectedId}
                tab={tab}
                onTabChange={(next) => openPolicy(selectedId, next)}
                unsaved={unsaved}
                onDeleted={() => openPolicy(undefined, 'editor')}
              />
            ) : (
              <EmptyState
                icon={MousePointerClickIcon}
                title={t('detail.selectTitle')}
                description={t('detail.selectDescription')}
              />
            )}
          </div>
        </TabsContent>
        <TabsContent value="bindings">
          <NamespaceBindingsView onOpenPolicy={(id) => openPolicy(id, 'bindings')} />
        </TabsContent>
        <TabsContent value="resolve">
          <ResolvePanel onOpenPolicy={(id) => openPolicy(id)} />
        </TabsContent>
        <TabsContent value="debugger">
          <DebuggerPanel />
        </TabsContent>
      </Tabs>
      <NewPolicyDialog
        open={creating}
        onOpenChange={setCreating}
        onCreated={(id) => openPolicy(id, 'editor')}
      />
    </>
  );
}

/** Policies: grouped list, editor (form/rules/YAML), versions, bindings, resolution and rule debugger. */
export default function PoliciesPage() {
  return (
    <RequirePermission permission={PERMISSIONS.policyRead}>
      <PoliciesContent />
    </RequirePermission>
  );
}
