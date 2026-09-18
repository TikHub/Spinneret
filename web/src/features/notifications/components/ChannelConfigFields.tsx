import { useTranslation } from 'react-i18next';

import { KeyValueEditor } from '@/components/KeyValueEditor';
import { FormField } from '@/components/ui/form';
import { Input } from '@/components/ui/input';

import {
  CHANNEL_FIELDS,
  MASKABLE_FIELDS,
  type ChannelConfigErrors,
  type ChannelConfigField,
  type ChannelConfigForm,
  type ChannelKind,
} from '../channelConfig';
import { MaskedValueInput } from './MaskedValueInput';

const REQUIRED: ReadonlySet<ChannelConfigField> = new Set(['url', 'webhookUrl', 'botToken', 'chatId']);
const SECRET_FIELDS: ReadonlySet<ChannelConfigField> = new Set(['secret', 'botToken']);

export interface ChannelConfigFieldsProps {
  kind: ChannelKind;
  value: ChannelConfigForm;
  onChange: (value: ChannelConfigForm) => void;
  /** Settings as read from the server (edit), to recognize untouched masked values. */
  initial: ChannelConfigForm;
  errors: ChannelConfigErrors;
  showErrors: boolean;
}

/** Kind-specific settings (webhook URL and headers, bot tokens, signing secrets). */
export function ChannelConfigFields({
  kind,
  value,
  onChange,
  initial,
  errors,
  showErrors,
}: ChannelConfigFieldsProps) {
  const { t } = useTranslation('notifications');
  const set = (field: Exclude<ChannelConfigField, 'headers'>, next: string) =>
    onChange({ ...value, [field]: next });
  const errorFor = (field: ChannelConfigField) => {
    const error = errors[field];
    return showErrors && error ? t(`validation.config.${error}`) : undefined;
  };

  return (
    <div className="grid gap-4">
      {CHANNEL_FIELDS[kind].map((field) => {
        if (field === 'headers') {
          return (
            <FormField
              key={field}
              label={t('config.headers')}
              error={errorFor(field)}
              description={t('config.headersHint')}
            >
              <div>
                <KeyValueEditor
                  value={value.headers}
                  onChange={(headers) => onChange({ ...value, headers })}
                  keyPlaceholder={t('config.headerName')}
                  valuePlaceholder={t('config.headerValue')}
                />
              </div>
            </FormField>
          );
        }
        const label = t(`config.${field}`);
        const description = t(`config.hints.${kind}.${field}`, { defaultValue: '' }) || undefined;
        return (
          <FormField
            key={field}
            label={label}
            required={REQUIRED.has(field)}
            error={errorFor(field)}
            description={description}
          >
            {MASKABLE_FIELDS.has(field) ? (
              <MaskedValueInput
                value={value[field]}
                onChange={(next) => set(field, next)}
                initial={initial[field]}
                secret={SECRET_FIELDS.has(field)}
                placeholder={t(`config.placeholders.${field}`, { defaultValue: '' }) || undefined}
              />
            ) : (
              <Input
                value={value[field]}
                className="font-mono"
                autoComplete="off"
                spellCheck={false}
                placeholder={t(`config.placeholders.${field}`, { defaultValue: '' }) || undefined}
                onChange={(e) => set(field, e.target.value)}
              />
            )}
          </FormField>
        );
      })}
    </div>
  );
}
