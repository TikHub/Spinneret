import { FileTextIcon } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { usePageTitle } from '@/app/pageTitle';
import { IdText } from '@/components/CopyButton';
import { EmptyState } from '@/components/EmptyState';
import { ErrorState } from '@/components/ErrorState';
import { TimeAgo } from '@/components/TimeAgo';
import { Skeleton } from '@/components/ui/skeleton';
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs';
import { type Policy } from '@/gen/spinneret/v1/policy_admin_pb';
import { isNotFound } from '@/lib/errors';

import { baseYaml, type DetailTab } from '../selectors';
import { usePolicy } from '../usePolicyQueries';
import { PolicyBindingsTab } from './bindings/PolicyBindingsTab';
import { DebuggerPanel } from './debugger/DebuggerPanel';
import { PolicyEditor, type UnsavedDrafts } from './editor/PolicyEditor';
import { KindBadge } from './labels';
import { VersionsTab } from './versions/VersionsTab';

export interface PolicyDetailProps {
  id: string;
  tab: DetailTab;
  onTabChange: (tab: DetailTab) => void;
  unsaved: UnsavedDrafts;
  onDeleted: () => void;
}

/** Selected policy: summary header and editor, versions, bindings and debugger tabs. */
export function PolicyDetail({ id, tab, onTabChange, unsaved, onDeleted }: PolicyDetailProps) {
  const { t } = useTranslation('policies');
  const query = usePolicy(id);
  const policy = query.data?.policy;
  usePageTitle(policy?.name);

  if (query.isLoading) {
    return (
      <div className="grid gap-3">
        <Skeleton className="h-20 w-full rounded-lg" />
        <Skeleton className="h-9 w-80" />
        <Skeleton className="h-[480px] w-full rounded-lg" />
      </div>
    );
  }
  if (query.isError && !policy) {
    return isNotFound(query.error) ? (
      <EmptyState
        icon={FileTextIcon}
        title={t('detail.notFound')}
        description={t('detail.notFoundDescription')}
      />
    ) : (
      <ErrorState error={query.error} onRetry={() => void query.refetch()} />
    );
  }
  if (!policy) return null;

  const debuggable = policy.kind === 'signal' || policy.kind === 'action';
  const activeTab = tab === 'debugger' && !debuggable ? 'editor' : tab;

  return (
    <div className="grid min-w-0 gap-3">
      <PolicyHeader policy={policy} />
      <Tabs value={activeTab} onValueChange={(v) => onTabChange(v as DetailTab)}>
        <TabsList>
          <TabsTrigger value="editor">{t('detail.tabs.editor')}</TabsTrigger>
          <TabsTrigger value="versions">
            {t('detail.tabs.versions')}
            <span className="text-muted-foreground tabular">{policy.currentVersion}</span>
          </TabsTrigger>
          <TabsTrigger value="bindings">
            {t('detail.tabs.bindings')}
            <span className="text-muted-foreground tabular">{policy.bindings.length}</span>
          </TabsTrigger>
          {debuggable && <TabsTrigger value="debugger">{t('detail.tabs.debugger')}</TabsTrigger>}
        </TabsList>
        <TabsContent value="editor">
          <PolicyEditor key={policy.id} policy={policy} unsaved={unsaved} onDeleted={onDeleted} />
        </TabsContent>
        <TabsContent value="versions">
          <VersionsTab key={policy.id} policy={policy} />
        </TabsContent>
        <TabsContent value="bindings">
          <PolicyBindingsTab policy={policy} />
        </TabsContent>
        {debuggable && (
          <TabsContent value="debugger">
            <DebuggerPanel
              key={policy.id}
              draftPolicy={policy}
              hasUnsavedChanges={(unsaved.get(policy.id) ?? baseYaml(policy)) !== baseYaml(policy)}
            />
          </TabsContent>
        )}
      </Tabs>
    </div>
  );
}

function PolicyHeader({ policy }: { policy: Policy }) {
  const { t } = useTranslation('policies');
  return (
    <header className="grid gap-2 rounded-lg border bg-card px-4 py-3">
      <div className="flex flex-wrap items-center gap-2">
        <h2 className="font-mono text-base font-semibold">{policy.name}</h2>
        <KindBadge kind={policy.kind} />
        <IdText value={policy.id} className="text-muted-foreground" />
      </div>
      {policy.description && <p className="text-sm text-muted-foreground">{policy.description}</p>}
      <dl className="flex flex-wrap gap-x-6 gap-y-1 text-xs text-muted-foreground">
        <div className="flex gap-1">
          <dt>{t('detail.published')}</dt>
          <dd className="font-medium text-foreground">
            {policy.currentVersion > 0 ? `v${policy.currentVersion}` : t('list.unpublished')}
          </dd>
        </div>
        <div className="flex gap-1">
          <dt>{t('detail.draft')}</dt>
          <dd className="text-foreground">
            {policy.hasDraft ? (
              <>
                <span className="font-mono">{policy.draftUpdatedBy || '—'}</span> ·{' '}
                <TimeAgo value={policy.draftUpdatedAt} />
              </>
            ) : (
              t('detail.noDraft')
            )}
          </dd>
        </div>
        <div className="flex gap-1">
          <dt>{t('detail.updated')}</dt>
          <dd className="text-foreground">
            <TimeAgo value={policy.updatedAt} past />
          </dd>
        </div>
        <div className="flex gap-1">
          <dt>{t('detail.createdBy')}</dt>
          <dd className="font-mono text-foreground">{policy.createdBy || '—'}</dd>
        </div>
      </dl>
    </header>
  );
}
