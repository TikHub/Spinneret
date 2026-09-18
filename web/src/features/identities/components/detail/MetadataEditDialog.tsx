import { useMutation } from '@tanstack/react-query';
import { LoaderCircleIcon } from 'lucide-react';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import { KeyValueEditor } from '@/components/KeyValueEditor';
import { TagsInput } from '@/components/TagsInput';
import { Button } from '@/components/ui/button';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { FormField, FormStack } from '@/components/ui/form';
import { Input } from '@/components/ui/input';
import { type Identity } from '@/gen/spinneret/v1/identity_admin_pb';
import { identityClient } from '@/lib/clients';
import { errorMessage } from '@/lib/errors';

import {
  buildUpdateIdentityRequest,
  METADATA_LIMITS,
  metadataFormFromIdentity,
  validateMetadata,
  type MetadataForm,
} from '../../metadata';
import { useInvalidateIdentityData } from '../../notify';
import { hasFormErrors } from '../../operations';

export interface MetadataEditDialogProps {
  identity: Identity;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

/** Edits region, tags, labels and account of an identity (UpdateIdentity with changed fields only). */
export function MetadataEditDialog({ identity, open, onOpenChange }: MetadataEditDialogProps) {
  const { t } = useTranslation('identities');
  const invalidate = useInvalidateIdentityData();
  const [form, setForm] = useState<MetadataForm>(() => metadataFormFromIdentity(identity));
  const errors = validateMetadata(form);
  const request = buildUpdateIdentityRequest(identity, form);

  const mutation = useMutation({
    mutationFn: () => {
      if (!request) throw new Error('nothing changed');
      return identityClient.updateIdentity(request);
    },
    onSuccess: () => {
      toast.success(t('metadata.saved'));
      invalidate();
      onOpenChange(false);
    },
    onError: (err) => toast.error(errorMessage(err, t)),
  });

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (hasFormErrors(errors) || !request) return;
    mutation.mutate();
  };

  return (
    <Dialog open={open} onOpenChange={(next) => !mutation.isPending && onOpenChange(next)}>
      <DialogContent className="sm:max-w-xl">
        <form onSubmit={submit} className="grid gap-4">
          <DialogHeader>
            <DialogTitle>{t('metadata.title')}</DialogTitle>
            <DialogDescription>{t('metadata.description')}</DialogDescription>
          </DialogHeader>
          <FormStack className="gap-3">
            <FormField
              label={t('fields.region')}
              error={
                errors.region ? t('metadata.errors.tooLong', { max: METADATA_LIMITS.region }) : undefined
              }
            >
              <Input value={form.region} onChange={(e) => setForm({ ...form, region: e.target.value })} />
            </FormField>
            <FormField
              label={t('fields.tags')}
              error={
                errors.tags ? t('metadata.errors.tooManyTags', { max: METADATA_LIMITS.tags }) : undefined
              }
            >
              <TagsInput
                value={form.tags}
                onChange={(tags) => setForm({ ...form, tags })}
                maxTags={METADATA_LIMITS.tags}
                validate={(tag) => tag.length <= METADATA_LIMITS.tagLength}
              />
            </FormField>
            <div className="grid gap-1.5">
              <span className="text-sm font-medium">{t('fields.labels')}</span>
              <KeyValueEditor value={form.labels} onChange={(labels) => setForm({ ...form, labels })} />
              {errors.labels && (
                <p role="alert" className="text-xs text-destructive">
                  {t(`metadata.errors.labels.${errors.labels}`, {
                    max: METADATA_LIMITS.labels,
                    keyMax: METADATA_LIMITS.labelKey,
                    valueMax: METADATA_LIMITS.labelValue,
                  })}
                </p>
              )}
            </div>
            <FormField
              label={t('fields.accountRef')}
              description={t('metadata.accountHint')}
              error={
                errors.accountRef
                  ? t('metadata.errors.tooLong', { max: METADATA_LIMITS.accountRef })
                  : undefined
              }
            >
              <Input
                value={form.accountRef}
                className="font-mono"
                onChange={(e) => setForm({ ...form, accountRef: e.target.value })}
              />
            </FormField>
          </FormStack>
          <DialogFooter>
            <Button
              type="button"
              variant="ghost"
              onClick={() => onOpenChange(false)}
              disabled={mutation.isPending}
            >
              {t('common:actions.cancel')}
            </Button>
            <Button type="submit" disabled={mutation.isPending || hasFormErrors(errors) || !request}>
              {mutation.isPending && <LoaderCircleIcon className="animate-spin" />}
              {t('common:actions.save')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
