import { LoaderCircleIcon } from 'lucide-react';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';

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
import { FormErrorAlert } from '@/features/access/components/FormErrorAlert';

import {
  EMPTY_ENTITY_FORM,
  suggestSlug,
  validateEntityForm,
  type EntityFormMode,
  type EntityFormValues,
  type EntityKind,
} from '../validation';

export interface EntityFormDialogProps {
  kind: EntityKind;
  mode: EntityFormMode;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Current values when editing. */
  initial?: Pick<EntityFormValues, 'name' | 'displayName' | 'description'>;
  /** Tenant display name shown for namespaces. */
  tenantName?: string;
  pending: boolean;
  error: unknown;
  onSubmit: (values: EntityFormValues) => void;
}

/** Create or edit form for tenants and namespaces (slug, display name, description). */
export function EntityFormDialog({
  kind,
  mode,
  open,
  onOpenChange,
  initial,
  tenantName,
  ...form
}: EntityFormDialogProps) {
  const { t } = useTranslation('tenants');
  return (
    <Dialog open={open} onOpenChange={(next) => !form.pending && onOpenChange(next)}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t(`form.${kind}.${mode}Title`, { name: initial?.name ?? '' })}</DialogTitle>
          <DialogDescription>
            {t(`form.${kind}.${mode}Description`, { tenant: tenantName ?? '' })}
          </DialogDescription>
        </DialogHeader>
        <EntityForm
          kind={kind}
          mode={mode}
          initial={initial}
          onCancel={() => onOpenChange(false)}
          {...form}
        />
      </DialogContent>
    </Dialog>
  );
}

interface EntityFormProps extends Pick<
  EntityFormDialogProps,
  'kind' | 'mode' | 'initial' | 'pending' | 'error' | 'onSubmit'
> {
  onCancel: () => void;
}

function EntityForm({ kind, mode, initial, pending, error, onSubmit, onCancel }: EntityFormProps) {
  const { t } = useTranslation('tenants');
  const [values, setValues] = useState<EntityFormValues>({ ...EMPTY_ENTITY_FORM, ...initial });
  const [nameTouched, setNameTouched] = useState(mode === 'edit');
  const [submitted, setSubmitted] = useState(false);
  const errors = validateEntityForm(values, kind, mode);
  const shown = submitted ? errors : {};

  const setDisplayName = (displayName: string) =>
    setValues((prev) => ({
      ...prev,
      displayName,
      // Suggest a slug from the display name until the slug is edited by hand.
      name: nameTouched ? prev.name : suggestSlug(displayName),
    }));

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setSubmitted(true);
    if (Object.values(errors).some(Boolean)) return;
    onSubmit({ ...values, ownerUserId: values.ownerUserId.trim() });
  };

  return (
    <form className="grid gap-4" onSubmit={submit} noValidate>
      <FormField label={t('form.displayName')} error={shown.displayName && t('form.errors.displayName')}>
        <Input value={values.displayName} onChange={(e) => setDisplayName(e.target.value)} autoFocus />
      </FormField>
      <FormField
        label={t('form.name')}
        required={mode === 'create'}
        description={mode === 'create' ? t('form.nameHint') : t('form.nameImmutable')}
        error={shown.name && t(`form.errors.name.${shown.name}`)}
      >
        <Input
          value={values.name}
          onChange={(e) => {
            setNameTouched(true);
            setValues((prev) => ({ ...prev, name: e.target.value }));
          }}
          readOnly={mode === 'edit'}
          disabled={mode === 'edit'}
          spellCheck={false}
          autoComplete="off"
          className="font-mono"
        />
      </FormField>
      <FormField label={t('form.description')} error={shown.description && t('form.errors.description')}>
        <Textarea
          value={values.description}
          onChange={(e) => setValues((prev) => ({ ...prev, description: e.target.value }))}
          rows={3}
        />
      </FormField>
      {kind === 'tenant' && mode === 'create' && (
        <FormField
          label={t('form.ownerUserId')}
          description={t('form.ownerUserIdHint')}
          error={shown.ownerUserId && t(`form.errors.ownerUserId.${shown.ownerUserId}`)}
        >
          <Input
            value={values.ownerUserId}
            onChange={(e) => setValues((prev) => ({ ...prev, ownerUserId: e.target.value }))}
            placeholder="usr_…"
            spellCheck={false}
            autoComplete="off"
            className="font-mono"
          />
        </FormField>
      )}
      <FormErrorAlert error={error} />
      <DialogFooter>
        <Button type="button" variant="outline" onClick={onCancel} disabled={pending}>
          {t('common:actions.cancel')}
        </Button>
        <Button type="submit" disabled={pending}>
          {pending && <LoaderCircleIcon className="animate-spin" />}
          {mode === 'create' ? t('common:actions.create') : t('common:actions.save')}
        </Button>
      </DialogFooter>
    </form>
  );
}
