import { useRef } from 'react';
import { useTranslation } from 'react-i18next';

import { SimpleTooltip } from '@/components/ui/tooltip';
import { FormField } from '@/components/ui/form';
import { Input } from '@/components/ui/input';

import { PROXY_LIMITS, SESSION_PLACEHOLDERS, validateSessionTemplate } from '../proxyForm';

export interface SessionTemplateFieldProps {
  value: string;
  onChange: (value: string) => void;
  disabled?: boolean;
}

/** Session template input with placeholder chips (click inserts at the cursor) and inline validation. */
export function SessionTemplateField({ value, onChange, disabled }: SessionTemplateFieldProps) {
  const { t } = useTranslation('proxies');
  const inputRef = useRef<HTMLInputElement>(null);
  const issue = validateSessionTemplate(value.trim());

  const insert = (placeholder: string) => {
    const token = `{${placeholder}}`;
    const input = inputRef.current;
    const start = input?.selectionStart ?? value.length;
    const end = input?.selectionEnd ?? value.length;
    const next = `${value.slice(0, start)}${token}${value.slice(end)}`;
    onChange(next);
    requestAnimationFrame(() => {
      input?.focus();
      input?.setSelectionRange(start + token.length, start + token.length);
    });
  };

  const error = issue
    ? t(`sessionTemplate.errors.${issue.code}`, {
        max: PROXY_LIMITS.sessionTemplate,
        name: issue.code === 'unknown_placeholder' ? `{${issue.name}}` : '',
      })
    : undefined;

  return (
    <div className="grid gap-1.5">
      <FormField label={t('sessionTemplate.label')} error={error}>
        <Input
          ref={inputRef}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          placeholder={t('sessionTemplate.placeholder')}
          className="font-mono"
          spellCheck={false}
          autoComplete="off"
          disabled={disabled}
        />
      </FormField>
      <div className="grid gap-1 text-xs text-muted-foreground">
        <span>{t('sessionTemplate.help')}</span>
        <div className="flex flex-wrap gap-1">
          {SESSION_PLACEHOLDERS.map((placeholder) => (
            <SimpleTooltip key={placeholder} content={t(`sessionTemplate.placeholders.${placeholder}`)}>
              <button
                type="button"
                disabled={disabled}
                onClick={() => insert(placeholder)}
                className="rounded border bg-muted/50 px-1.5 py-0.5 font-mono text-[11px] text-foreground hover:bg-accent disabled:opacity-50"
                aria-label={t('sessionTemplate.insert', { placeholder: `{${placeholder}}` })}
              >
                {`{${placeholder}}`}
              </button>
            </SimpleTooltip>
          ))}
        </div>
      </div>
    </div>
  );
}
