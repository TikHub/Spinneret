import { useMutation, useQueryClient } from '@tanstack/react-query';
import { LoaderCircleIcon } from 'lucide-react';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import { DurationInput } from '@/components/DurationInput';
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
import { Textarea } from '@/components/ui/textarea';
import { type CreateTokenResponse } from '@/gen/spinneret/v1/access_admin_pb';
import { accessClient } from '@/lib/clients';
import { useNow } from '@/lib/clock';
import { errorMessage } from '@/lib/errors';
import { sensitiveMutation } from '@/lib/sensitiveMutation';
import { formatDateTime, toTimestamp } from '@/lib/time';

import { hasErrors } from '../../forms';
import { isIpOrCidr } from '../../ip';
import { scopesFromRows } from '../../scopes';
import {
  EMPTY_TOKEN_FORM,
  MAX_IP_ALLOWLIST,
  MAX_RATE_LIMIT_RPS,
  parseRateLimit,
  resolveExpiry,
  TOKEN_EXPIRY_PRESETS,
  validateTokenForm,
  type TokenFormValues,
} from '../../tokens';
import { FormErrorAlert } from '../FormErrorAlert';

import { ScopeBuilder } from './ScopeBuilder';

export interface CreateTokenDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  namespace: string;
  /** Called with the response; the caller shows the plaintext once. */
  onCreated: (response: CreateTokenResponse) => void;
}

/** Dialog creating an API token in the active namespace. */
export function CreateTokenDialog({ open, onOpenChange, namespace, onCreated }: CreateTokenDialogProps) {
  const { t } = useTranslation('access');
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{t('tokens.form.title')}</DialogTitle>
          <DialogDescription>{t('tokens.form.description', { namespace })}</DialogDescription>
        </DialogHeader>
        {/* Mounted only while open, so every opening starts with an empty form. */}
        <CreateTokenForm namespace={namespace} onCancel={() => onOpenChange(false)} onCreated={onCreated} />
      </DialogContent>
    </Dialog>
  );
}

interface CreateTokenFormProps {
  namespace: string;
  onCancel: () => void;
  onCreated: (response: CreateTokenResponse) => void;
}

/** CreateToken request of a submitted form; a relative expiry counts from the submit time. */
function createTokenRequest(namespace: string, form: TokenFormValues) {
  const expiry = resolveExpiry(form.expiresIn, Date.now());
  return {
    namespace,
    name: form.name.trim(),
    description: form.description.trim(),
    scopes: scopesFromRows(form.scopes),
    ipAllowlist: form.ipAllowlist,
    rateLimitRps: parseRateLimit(form.rateLimit) ?? 0,
    expiresAt: expiry instanceof Date ? toTimestamp(expiry) : undefined,
  };
}

function CreateTokenForm({ namespace, onCancel, onCreated }: CreateTokenFormProps) {
  const { t } = useTranslation('access');
  const queryClient = useQueryClient();
  const now = useNow();
  const [values, setValues] = useState<TokenFormValues>(EMPTY_TOKEN_FORM);
  const [submitted, setSubmitted] = useState(false);

  // The response carries the plaintext token: drop it from the mutation cache
  // as soon as the form unmounts instead of keeping it for the default gcTime.
  const mutation = useMutation(
    sensitiveMutation({
      mutationFn: (form: TokenFormValues) => accessClient.createToken(createTokenRequest(namespace, form)),
      onSuccess: (response) => {
        toast.success(t('tokens.form.created', { name: response.token?.name ?? '' }));
        void queryClient.invalidateQueries({ queryKey: ['access'] });
        onCreated(response);
      },
      onError: (err) => toast.error(errorMessage(err, t)),
    }),
  );

  const errors = validateTokenForm(values, now);
  const shown = submitted ? errors : {};
  const set = <K extends keyof TokenFormValues>(field: K, value: TokenFormValues[K]) =>
    setValues((prev) => ({ ...prev, [field]: value }));

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setSubmitted(true);
    if (hasErrors(validateTokenForm(values, Date.now()))) return;
    mutation.mutate(values);
  };

  const expiry = resolveExpiry(values.expiresIn, now);
  const expiryHint =
    expiry instanceof Date
      ? t('tokens.form.expiresAt', { time: formatDateTime(expiry, false) })
      : expiry === undefined
        ? t('tokens.form.neverExpires')
        : undefined;

  return (
    <form className="grid gap-4" onSubmit={submit} noValidate>
      <div className="grid gap-4 sm:grid-cols-2">
        <FormField
          label={t('tokens.form.name')}
          required
          error={shown.name && t(`tokens.form.errors.name.${shown.name}`)}
        >
          <Input
            value={values.name}
            onChange={(e) => set('name', e.target.value)}
            placeholder={t('tokens.form.namePlaceholder')}
            autoComplete="off"
            autoFocus
          />
        </FormField>
        <FormField
          label={t('tokens.form.rateLimit')}
          description={t('tokens.form.rateLimitHint')}
          error={shown.rateLimit && t('tokens.form.errors.rateLimit', { max: MAX_RATE_LIMIT_RPS })}
        >
          <Input
            type="number"
            inputMode="numeric"
            min={0}
            max={MAX_RATE_LIMIT_RPS}
            value={values.rateLimit}
            onChange={(e) => set('rateLimit', e.target.value)}
            placeholder={t('tokens.form.rateLimitPlaceholder')}
            className="tabular"
          />
        </FormField>
      </div>
      <FormField
        label={t('tokens.form.descriptionLabel')}
        error={shown.description && t('tokens.form.errors.description')}
      >
        <Textarea
          value={values.description}
          onChange={(e) => set('description', e.target.value)}
          rows={2}
          className="min-h-14"
        />
      </FormField>

      <fieldset className="grid gap-1.5">
        <legend className="mb-1.5 text-sm font-medium">
          {t('tokens.form.scopes')}
          <span className="text-destructive">*</span>
        </legend>
        <ScopeBuilder
          rows={values.scopes}
          onChange={(rows) => set('scopes', rows)}
          namespace={namespace}
          showErrors={submitted}
        />
        {shown.scopes && (
          <p role="alert" className="text-xs text-destructive">
            {t(`tokens.form.errors.scopes.${shown.scopes}`)}
          </p>
        )}
      </fieldset>

      <div className="grid gap-4 sm:grid-cols-2">
        <FormField
          label={t('tokens.form.ipAllowlist')}
          description={t('tokens.form.ipAllowlistHint')}
          error={shown.ipAllowlist && t(`tokens.form.errors.ipAllowlist.${shown.ipAllowlist}`)}
        >
          <TagsInput
            value={values.ipAllowlist}
            onChange={(tags) => set('ipAllowlist', tags)}
            validate={isIpOrCidr}
            maxTags={MAX_IP_ALLOWLIST}
            placeholder={t('tokens.form.ipAllowlistPlaceholder')}
          />
        </FormField>
        <FormField
          label={t('tokens.form.expiresIn')}
          description={expiryHint}
          error={shown.expiresIn && t(`tokens.form.errors.expiresIn.${shown.expiresIn}`)}
        >
          <DurationInput
            value={values.expiresIn}
            onChange={(value) => set('expiresIn', value)}
            allowEmpty
            presets={TOKEN_EXPIRY_PRESETS}
            placeholder={t('tokens.form.expiresInPlaceholder')}
          />
        </FormField>
      </div>

      <FormErrorAlert error={mutation.error} />

      <DialogFooter>
        <Button type="button" variant="outline" onClick={onCancel} disabled={mutation.isPending}>
          {t('common:actions.cancel')}
        </Button>
        <Button type="submit" disabled={mutation.isPending || (submitted && hasErrors(errors))}>
          {mutation.isPending && <LoaderCircleIcon className="animate-spin" />}
          {t('tokens.form.submit')}
        </Button>
      </DialogFooter>
    </form>
  );
}
