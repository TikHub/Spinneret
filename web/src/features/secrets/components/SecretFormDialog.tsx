import { useMutation, useQueryClient } from '@tanstack/react-query';
import { LoaderCircleIcon } from 'lucide-react';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import { useAuth } from '@/app/auth/AuthContext';
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
import { FormField } from '@/components/ui/form';
import { Input } from '@/components/ui/input';
import { Switch } from '@/components/ui/switch';
import { type SecretInfo } from '@/gen/spinneret/v1/secret_admin_pb';
import { secretAdminClient } from '@/lib/clients';
import { useNow } from '@/lib/clock';
import { errorMessage } from '@/lib/errors';
import { sensitiveMutation } from '@/lib/sensitiveMutation';

import {
  createSecretRequest,
  EMPTY_SECRET_FORM,
  formFromSecret,
  hasErrors,
  MAX_SECRET_DESCRIPTION_LENGTH,
  MAX_SECRET_TAGS,
  updateSecretRequest,
  validateSecretForm,
  type SecretFormValues,
} from '../secretForm';
import { isValidSecretTag } from '../secretPath';
import { SecretValueInput } from './SecretValueInput';

export interface SecretFormDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Secret to update; create mode when undefined. */
  secret?: SecretInfo;
  /** Prefilled path prefix (the selected folder) in create mode. */
  pathPrefix?: string;
  onSaved?: (secret: SecretInfo | undefined) => void;
}

interface SecretFormProps extends Omit<SecretFormDialogProps, 'open'> {
  onPendingChange: (pending: boolean) => void;
}

/**
 * Form body. It is mounted only while the dialog is open, so the plaintext
 * value (form state and mutation variables) is dropped when the dialog closes.
 */
function SecretForm({ onOpenChange, secret, pathPrefix = '', onSaved, onPendingChange }: SecretFormProps) {
  const { t } = useTranslation('secrets');
  const { namespaceName } = useAuth();
  const queryClient = useQueryClient();
  const mode = secret ? 'edit' : 'create';
  const [initial] = useState<SecretFormValues>(() =>
    secret ? formFromSecret(secret) : { ...EMPTY_SECRET_FORM, path: pathPrefix },
  );
  const [values, setValues] = useState<SecretFormValues>(initial);
  const [touched, setTouched] = useState(false);
  const now = useNow();
  const errors = validateSecretForm(values, mode, now, secret ? initial : undefined);
  const shown = touched ? errors : {};
  const set = <K extends keyof SecretFormValues>(field: K, value: SecretFormValues[K]) =>
    setValues((prev) => ({ ...prev, [field]: value }));

  // The variables hold the plaintext value: drop the mutation from the cache as soon as the form unmounts
  // (the default keeps it for 5 minutes).
  const mutation = useMutation(
    sensitiveMutation({
      mutationFn: async (form: SecretFormValues) => {
        if (!secret) {
          const res = await secretAdminClient.createSecret(createSecretRequest(namespaceName ?? '', form));
          return { secret: res.secret, versioned: true, unchanged: false };
        }
        const request = updateSecretRequest(secret, form);
        if (!request) return { secret, versioned: false, unchanged: true };
        const res = await secretAdminClient.updateSecret(request);
        return { secret: res.secret, versioned: request.value !== undefined, unchanged: false };
      },
      onMutate: () => onPendingChange(true),
      onSettled: () => onPendingChange(false),
      onSuccess: (res) => {
        if (res.unchanged) {
          toast.info(t('form.noChanges'));
        } else if (!secret) {
          toast.success(t('form.created', { path: res.secret?.path ?? '' }));
        } else {
          toast.success(
            res.versioned
              ? t('form.updatedVersion', { path: secret.path, version: res.secret?.currentVersion ?? 0 })
              : t('form.updated', { path: secret.path }),
          );
        }
        void queryClient.invalidateQueries({ queryKey: ['secrets'] });
        onOpenChange(false);
        onSaved?.(res.secret);
      },
      onError: (err) => toast.error(errorMessage(err, t)),
    }),
  );

  const submit = (event: FormEvent) => {
    event.preventDefault();
    setTouched(true);
    if (!hasErrors(errors)) mutation.mutate(values);
  };

  return (
    <form onSubmit={submit} className="grid gap-4" noValidate autoComplete="off">
      <DialogHeader>
        <DialogTitle>
          {secret ? t('form.editTitle', { path: secret.path }) : t('form.createTitle')}
        </DialogTitle>
        <DialogDescription>
          {secret ? t('form.editDescription') : t('form.createDescription', { namespace: namespaceName })}
        </DialogDescription>
      </DialogHeader>
      <FormField
        label={t('fields.path')}
        required={!secret}
        error={shown.path ? t(`validation.path.${shown.path}`) : undefined}
        description={secret ? undefined : t('form.pathHint')}
      >
        <Input
          value={values.path}
          readOnly={Boolean(secret)}
          className="font-mono"
          maxLength={256}
          autoComplete="off"
          spellCheck={false}
          onChange={(e) => set('path', e.target.value)}
        />
      </FormField>
      <FormField
        label={secret ? t('fields.newValue') : t('fields.value')}
        required={!secret}
        error={shown.value ? t(`validation.value.${shown.value}`) : undefined}
        description={
          secret ? t('form.newValueHint', { version: secret.currentVersion + 1 }) : t('form.valueHint')
        }
      >
        <SecretValueInput
          value={values.value}
          onChange={(v) => set('value', v)}
          placeholder={secret ? t('form.keepValue') : undefined}
        />
      </FormField>
      <FormField label={t('fields.description')}>
        <Input
          value={values.description}
          maxLength={MAX_SECRET_DESCRIPTION_LENGTH}
          onChange={(e) => set('description', e.target.value)}
        />
      </FormField>
      <FormField
        label={t('fields.tags')}
        error={shown.tags ? t(`validation.tags.${shown.tags}`, { max: MAX_SECRET_TAGS }) : undefined}
      >
        <TagsInput
          value={values.tags}
          onChange={(tags) => set('tags', tags)}
          maxTags={MAX_SECRET_TAGS}
          validate={isValidSecretTag}
        />
      </FormField>
      <div className="grid gap-3 rounded-md border p-3">
        <FormField inline label={t('form.expires')}>
          <Switch checked={values.expires} onCheckedChange={(v) => set('expires', v)} />
        </FormField>
        {values.expires && (
          <FormField
            label={t('fields.expiresAt')}
            required
            error={shown.expiresAt ? t(`validation.expiresAt.${shown.expiresAt}`) : undefined}
            description={t('form.expiresHint')}
          >
            <Input
              type="datetime-local"
              value={values.expiresAt}
              onChange={(e) => set('expiresAt', e.target.value)}
              className="w-60"
            />
          </FormField>
        )}
      </div>
      <DialogFooter>
        <Button
          type="button"
          variant="outline"
          disabled={mutation.isPending}
          onClick={() => onOpenChange(false)}
        >
          {t('common:actions.cancel')}
        </Button>
        <Button type="submit" disabled={mutation.isPending || !namespaceName}>
          {mutation.isPending && <LoaderCircleIcon className="animate-spin" />}
          {secret ? t('common:actions.save') : t('common:actions.create')}
        </Button>
      </DialogFooter>
    </form>
  );
}

/** Create a secret, or update its metadata and store a new version. */
export function SecretFormDialog({ open, onOpenChange, ...props }: SecretFormDialogProps) {
  const [pending, setPending] = useState(false);
  return (
    <Dialog open={open} onOpenChange={(next) => !pending && onOpenChange(next)}>
      <DialogContent className="sm:max-w-xl">
        <SecretForm
          key={props.secret?.id ?? 'new'}
          {...props}
          onOpenChange={onOpenChange}
          onPendingChange={setPending}
        />
      </DialogContent>
    </Dialog>
  );
}
