import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import { DiffView } from '@/components/editor/DiffView';
import { FormField } from '@/components/ui/form';
import { Textarea } from '@/components/ui/textarea';
import { type ConfigItemInfo } from '@/gen/spinneret/v1/config_admin_pb';
import { configAdminClient } from '@/lib/clients';

import { itemLabel, MAX_CONFIG_COMMENT_LENGTH } from '../configModel';
import {
  draftRequest,
  preparePublishDiff,
  type ConfigDraftValues,
  type ConfigEditorState,
} from '../draftState';
import { WideConfirmDialog } from './WideConfirmDialog';

export interface PublishConfigDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  item: ConfigItemInfo;
  state: ConfigEditorState;
  /** Called with the saved draft (when local edits were saved first) and the published item. */
  onPublished: (result: {
    saved?: { sent: ConfigDraftValues; item: ConfigItemInfo };
    item?: ConfigItemInfo;
  }) => void;
}

/** Publish confirmation: diff between the published content and the draft, plus a comment. */
export function PublishConfigDialog({
  open,
  onOpenChange,
  item,
  state,
  onPublished,
}: PublishConfigDialogProps) {
  const { t } = useTranslation('config');
  const [comment, setComment] = useState('');
  const [wasOpen, setWasOpen] = useState(open);
  if (open !== wasOpen) {
    setWasOpen(open);
    if (open) setComment('');
  }
  // The item panel re-renders on every editor keystroke; prepare the diff (an LCS over lines) only
  // while the dialog is open and keep the last one for the closing animation.
  const live = useMemo(() => (open ? preparePublishDiff(item, state) : undefined), [open, item, state]);
  const [shown, setShown] = useState(live);
  if (live !== undefined && live !== shown) setShown(live);
  const diff = live ?? shown;
  if (!diff) return null;

  const publish = async () => {
    let saved: { sent: ConfigDraftValues; item: ConfigItemInfo } | undefined;
    if (diff.savesDraftFirst) {
      const sent = state.values;
      const res = await configAdminClient.saveConfigDraft(draftRequest(item.id, sent, item));
      if (res.item) saved = { sent, item: res.item };
    }
    try {
      const res = await configAdminClient.publishConfig({
        id: item.id,
        comment: comment.trim(),
        expectedVersion: diff.expectedVersion,
      });
      toast.success(t('publish.success', { label: itemLabel(item), version: res.version }));
      onPublished({ saved, item: res.item });
    } catch (err) {
      // The draft was saved even though publishing failed; keep the editor in sync.
      if (saved) onPublished({ saved });
      throw err;
    }
  };

  return (
    <WideConfirmDialog
      open={open}
      onOpenChange={onOpenChange}
      title={t('publish.title', { label: itemLabel(item) })}
      description={
        <div className="grid gap-1">
          <p>
            {diff.firstPublish
              ? t('publish.firstDescription')
              : t('publish.description', { current: item.currentVersion, next: diff.nextVersion })}
          </p>
          {diff.savesDraftFirst && <p className="text-foreground">{t('publish.savesDraftFirst')}</p>}
        </div>
      }
      confirmLabel={t('publish.confirm', { version: diff.nextVersion })}
      confirmDisabled={!diff.changed}
      onConfirm={publish}
    >
      <div className="grid gap-3">
        <div className="flex flex-wrap items-center gap-3 text-xs">
          <span className="text-muted-foreground">
            {diff.firstPublish
              ? t('publish.emptyOriginal')
              : t('publish.original', { version: item.currentVersion })}
            {' → '}
            {t('publish.modified')}
          </span>
          <span className="font-medium text-emerald-600 tabular dark:text-emerald-400">
            +{diff.stats.added}
          </span>
          <span className="font-medium text-rose-600 tabular dark:text-rose-400">−{diff.stats.removed}</span>
          {!diff.changed && <span className="text-destructive">{t('publish.noChanges')}</span>}
        </div>
        <DiffView original={diff.original} modified={diff.modified} language={diff.language} height={360} />
        <FormField label={t('fields.comment')} description={t('publish.commentHint')}>
          <Textarea
            value={comment}
            rows={2}
            maxLength={MAX_CONFIG_COMMENT_LENGTH}
            onChange={(e) => setComment(e.target.value)}
          />
        </FormField>
      </div>
    </WideConfirmDialog>
  );
}
