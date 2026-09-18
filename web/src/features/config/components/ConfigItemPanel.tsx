import { useMutation } from '@tanstack/react-query';
import { FileQuestionIcon, TriangleAlertIcon } from 'lucide-react';
import { useCallback, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import { useAuth } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { EmptyState } from '@/components/EmptyState';
import { ErrorState } from '@/components/ErrorState';
import { Button } from '@/components/ui/button';
import { Skeleton } from '@/components/ui/skeleton';
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs';
import { type ConfigItemInfo, type ConfigVersion } from '@/gen/spinneret/v1/config_admin_pb';
import { configAdminClient } from '@/lib/clients';
import { errorMessage, isNotFound } from '@/lib/errors';

import { isReadOnlyItem, itemLabel } from '../configModel';
import { draftRequest, type ConfigDraftValues } from '../draftState';
import {
  useConfigCacheRemove,
  useConfigCacheUpdate,
  useConfigItem,
  type ConfigSelection,
} from '../useConfigApi';
import { useConfigEditor } from '../useConfigEditor';
import { useUnsavedChangesGuard } from '../useUnsavedChangesGuard';
import { ConfigContentTab } from './ConfigContentTab';
import { ConfigItemActions } from './ConfigItemActions';
import { ConfigItemHeader } from './ConfigItemHeader';
import { ConfigSchemaTab } from './ConfigSchemaTab';
import { ConfigVersionsTab } from './ConfigVersionsTab';
import { PublishConfigDialog } from './PublishConfigDialog';
import { RollbackConfigDialog } from './RollbackConfigDialog';
import { UnsavedChangesDialog } from './UnsavedChangesGuard';

type DialogKind = 'publish' | 'rollback' | 'delete' | undefined;

export interface ConfigItemPanelProps {
  selection: ConfigSelection;
  onDeleted: () => void;
  onClearSelection: () => void;
}

function PanelSkeleton() {
  return (
    <div className="grid gap-3" role="status">
      <Skeleton className="h-8 w-1/2" />
      <Skeleton className="h-4 w-1/3" />
      <Skeleton className="h-9 w-72" />
      <Skeleton className="h-96 w-full" />
    </div>
  );
}

/** Editor of the selected config item: content, schema, versions, draft/publish/rollback/delete. */
export function ConfigItemPanel({ selection, onDeleted, onClearSelection }: ConfigItemPanelProps) {
  const { t } = useTranslation('config');
  const { can } = useAuth();
  const query = useConfigItem(selection);
  const item = query.data ?? undefined;
  const editor = useConfigEditor(item);
  const { blocker, allowNextNavigation } = useUnsavedChangesGuard(editor.dirty);
  const updateCache = useConfigCacheUpdate();
  const removeFromCache = useConfigCacheRemove();
  const [tab, setTab] = useState('content');
  const [dialog, setDialog] = useState<DialogKind>();
  const [rollbackVersion, setRollbackVersion] = useState<ConfigVersion>();

  const save = useMutation({
    mutationFn: ({ target, sent }: { target: ConfigItemInfo; sent: ConfigDraftValues }) =>
      configAdminClient.saveConfigDraft(draftRequest(target.id, sent, target)),
    onSuccess: (res, { sent }) => {
      if (res.item) editor.markSaved(sent, res.item);
      updateCache(res.item);
      toast.success(res.item && !res.item.hasDraft ? t('draft.matchesPublished') : t('draft.saved'));
    },
    onError: (err) => toast.error(errorMessage(err, t)),
  });

  const openRollback = useCallback((version?: ConfigVersion) => {
    setRollbackVersion(version);
    setDialog('rollback');
  }, []);

  if (query.isLoading) return <PanelSkeleton />;
  if (query.isError && !item) {
    if (isNotFound(query.error)) {
      return (
        <EmptyState
          icon={FileQuestionIcon}
          title={t('item.notFound')}
          description={t('item.notFoundDescription', { label: `${selection.group}/${selection.key}` })}
          action={
            <Button variant="outline" size="sm" onClick={onClearSelection}>
              {t('item.clearSelection')}
            </Button>
          }
        />
      );
    }
    return <ErrorState error={query.error} onRetry={() => void query.refetch()} />;
  }
  if (!item) return <PanelSkeleton />;

  const readOnly = isReadOnlyItem(item);
  const editable = !readOnly && can(PERMISSIONS.configWrite);
  const { state, dirty, validation } = editor;

  const deleteItem = async () => {
    await configAdminClient.deleteConfigItem({ id: item.id });
    toast.success(t('delete.success', { label: itemLabel(item) }));
    editor.discard();
    allowNextNavigation();
    onDeleted();
    removeFromCache({ group: item.group, key: item.key });
  };

  return (
    <div className="grid gap-3">
      <ConfigItemHeader
        item={item}
        readOnly={readOnly}
        dirty={dirty}
        actions={
          <ConfigItemActions
            item={item}
            readOnly={readOnly}
            dirty={dirty}
            blocking={validation.blocking}
            saving={save.isPending}
            onSave={() => save.mutate({ target: item, sent: state.values })}
            onDiscard={editor.discard}
            onPublish={() => setDialog('publish')}
            onRollback={() => openRollback(undefined)}
            onDelete={() => setDialog('delete')}
          />
        }
      />
      {editor.remoteChanged && (
        <div
          role="alert"
          className="flex flex-wrap items-center gap-2 rounded-md border border-amber-500/30 bg-amber-500/5 px-3 py-2 text-sm text-amber-700 dark:text-amber-400"
        >
          <TriangleAlertIcon className="size-4 shrink-0" aria-hidden />
          <span className="flex-1">{t('item.remoteChanged')}</span>
          <Button variant="outline" size="sm" onClick={editor.discard}>
            {t('item.reloadDiscard')}
          </Button>
        </div>
      )}
      {readOnly && <p className="text-sm text-muted-foreground">{t('item.readOnlyHint')}</p>}
      <Tabs value={tab} onValueChange={setTab}>
        <TabsList>
          <TabsTrigger value="content">{t('tabs.content')}</TabsTrigger>
          <TabsTrigger value="schema">{t('tabs.schema')}</TabsTrigger>
          <TabsTrigger value="versions" disabled={item.id === ''}>
            {t('tabs.versions')}
          </TabsTrigger>
        </TabsList>
        <TabsContent value="content">
          <ConfigContentTab
            item={item}
            content={state.values.content}
            onChange={(v) => editor.setField('content', v)}
            readOnly={!editable}
            validation={validation}
          />
        </TabsContent>
        <TabsContent value="schema">
          <ConfigSchemaTab
            item={item}
            values={state.values}
            onChange={editor.setField}
            readOnly={!editable}
            validation={validation}
          />
        </TabsContent>
        <TabsContent value="versions">
          {item.id !== '' && <ConfigVersionsTab item={item} onRollback={openRollback} />}
        </TabsContent>
      </Tabs>

      <PublishConfigDialog
        open={dialog === 'publish'}
        onOpenChange={(open) => setDialog(open ? 'publish' : undefined)}
        item={item}
        state={state}
        onPublished={({ saved, item: published }) => {
          if (saved) {
            editor.markSaved(saved.sent, saved.item);
            updateCache(saved.item);
          }
          if (published) updateCache(published);
        }}
      />
      <RollbackConfigDialog
        open={dialog === 'rollback'}
        onOpenChange={(open) => setDialog(open ? 'rollback' : undefined)}
        item={item}
        preselected={rollbackVersion}
        onRolledBack={updateCache}
      />
      <ConfirmDialog
        open={dialog === 'delete'}
        onOpenChange={(open) => setDialog(open ? 'delete' : undefined)}
        title={t('delete.title', { label: itemLabel(item) })}
        description={t('delete.description')}
        confirmLabel={t('common:actions.delete')}
        destructive
        confirmText={itemLabel(item)}
        onConfirm={deleteItem}
      />
      <UnsavedChangesDialog blocker={blocker} />
    </div>
  );
}
