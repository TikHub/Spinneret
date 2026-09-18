import { useState } from 'react';
import { useTranslation } from 'react-i18next';

import { ConfirmDialog } from '@/components/ConfirmDialog';
import { FormField } from '@/components/ui/form';
import { Textarea } from '@/components/ui/textarea';
import { type Policy } from '@/gen/spinneret/v1/policy_admin_pb';

import { usePublishPolicy, useSaveDraft } from '../../usePolicyMutations';

export interface PublishDialogProps {
  policy: Policy;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Local YAML when it differs from the stored draft (saved first). */
  unsavedYaml: string | undefined;
  onPublished: () => void;
}

/** Publishes the draft (saving unsaved changes first) with a comment and an optimistic version check. */
export function PublishDialog({ policy, open, onOpenChange, unsavedYaml, onPublished }: PublishDialogProps) {
  const { t } = useTranslation('policies');
  const [comment, setComment] = useState('');
  const saveDraft = useSaveDraft();
  const publish = usePublishPolicy();

  const confirm = async () => {
    if (unsavedYaml !== undefined) {
      await saveDraft.mutateAsync({ id: policy.id, yaml: unsavedYaml });
    }
    await publish.mutateAsync({
      id: policy.id,
      comment: comment.trim(),
      expectedVersion: policy.currentVersion,
    });
    setComment('');
    onPublished();
  };

  return (
    <ConfirmDialog
      open={open}
      onOpenChange={onOpenChange}
      title={t('publish.title', { name: policy.name })}
      description={
        <>
          {t('publish.description', { version: policy.currentVersion + 1 })}{' '}
          {unsavedYaml !== undefined && <strong>{t('publish.savesFirst')}</strong>}
        </>
      }
      confirmLabel={t('publish.confirm')}
      onConfirm={confirm}
    >
      <FormField label={t('fields.comment')} description={t('publish.commentHint')}>
        <Textarea
          value={comment}
          onChange={(e) => setComment(e.target.value)}
          maxLength={1024}
          rows={3}
          placeholder={t('publish.commentPlaceholder')}
        />
      </FormField>
    </ConfirmDialog>
  );
}
