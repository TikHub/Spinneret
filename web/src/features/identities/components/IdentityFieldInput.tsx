import { useTranslation } from 'react-i18next';

import { FormField } from '@/components/ui/form';
import { Input } from '@/components/ui/input';
import { Switch } from '@/components/ui/switch';
import { Textarea } from '@/components/ui/textarea';
import { type IdentityField } from '@/gen/spinneret/v1/identity_admin_pb';

import { type FieldError } from '../newIdentity';

export interface IdentityFieldInputProps {
  field: IdentityField;
  value: string;
  onChange: (value: string) => void;
  error?: FieldError;
  disabled?: boolean;
}

/** Field types whose value is pasted rather than typed, so they get a textarea. */
const MULTILINE = new Set(['cookie_map', 'json']);

/**
 * One control for one payload field of an identity type, chosen from the
 * field's declared type.
 */
export function IdentityFieldInput({ field, value, onChange, error, disabled }: IdentityFieldInputProps) {
  const { t } = useTranslation('identities');

  const label = (
    <span className="flex items-center gap-1.5">
      <span className="font-mono text-xs">{field.name}</span>
      <span className="text-xs text-muted-foreground">{field.type}</span>
      {field.sensitive && <span className="text-xs text-muted-foreground">{t('newIdentity.sensitive')}</span>}
    </span>
  );
  const description =
    field.description || t(`newIdentity.hints.${field.type}`, { defaultValue: '' }) || undefined;
  const errorText = error ? t(`newIdentity.errors.${error}`) : undefined;

  if (field.type === 'bool') {
    return (
      <FormField label={label} description={description} error={errorText} inline>
        <Switch
          checked={value === 'true'}
          onCheckedChange={(checked) => onChange(String(checked))}
          disabled={disabled}
        />
      </FormField>
    );
  }

  if (MULTILINE.has(field.type)) {
    return (
      <FormField label={label} description={description} error={errorText} required={field.required}>
        <Textarea
          value={value}
          onChange={(e) => onChange(e.target.value)}
          disabled={disabled}
          rows={field.type === 'cookie_map' ? 5 : 4}
          spellCheck={false}
          className="font-mono text-xs"
          placeholder={t(`newIdentity.placeholders.${field.type}`, { defaultValue: '' })}
        />
      </FormField>
    );
  }

  return (
    <FormField label={label} description={description} error={errorText} required={field.required}>
      <Input
        value={value}
        onChange={(e) => onChange(e.target.value)}
        disabled={disabled}
        inputMode={field.type === 'number' ? 'decimal' : undefined}
        spellCheck={false}
        placeholder={t(`newIdentity.placeholders.${field.type}`, { defaultValue: '' })}
      />
    </FormField>
  );
}
