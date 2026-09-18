import { useTranslation } from 'react-i18next';

import { TagsInput } from '@/components/TagsInput';
import { FormField } from '@/components/ui/form';
import { Input } from '@/components/ui/input';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';

import { type ImportDefaultsForm, type ImportIssue } from '../importProxies';
import { PROXY_KINDS } from '../proxyFilters';
import { PROXY_LIMITS } from '../proxyForm';
import { SessionTemplateField } from './SessionTemplateField';

/** Select value standing for "no default kind" (Radix items cannot be empty). */
const DEFAULT_KIND = '__default__';

export interface ImportDefaultsFieldsProps {
  value: ImportDefaultsForm;
  onChange: (value: ImportDefaultsForm) => void;
  issues: readonly ImportIssue[];
  disabled?: boolean;
}

/** Attribute defaults applied to imported rows that do not set them. */
export function ImportDefaultsFields({ value, onChange, issues, disabled }: ImportDefaultsFieldsProps) {
  const { t } = useTranslation('proxies');
  const set = (patch: Partial<ImportDefaultsForm>) => onChange({ ...value, ...patch });
  const tooLong = (issue: ImportIssue, max: number) =>
    issues.includes(issue) ? t('edit.errors.tooLong', { max }) : undefined;

  return (
    <fieldset className="grid gap-3" disabled={disabled}>
      <legend className="mb-1 text-sm font-medium">{t('import.defaults')}</legend>
      <div className="grid gap-3 sm:grid-cols-2">
        <FormField label={t('import.kind')}>
          <Select
            value={value.kind === '' ? DEFAULT_KIND : value.kind}
            onValueChange={(kind) => set({ kind: kind === DEFAULT_KIND ? '' : kind })}
            disabled={disabled}
          >
            <SelectTrigger className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={DEFAULT_KIND}>{t('import.kindDefault')}</SelectItem>
              {PROXY_KINDS.map((kind) => (
                <SelectItem key={kind} value={kind}>
                  {t(`kinds.${kind}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </FormField>
        <FormField
          label={t('import.maxConcurrency')}
          error={
            issues.includes('max_concurrency')
              ? t('edit.errors.maxConcurrency', { max: PROXY_LIMITS.maxConcurrency })
              : undefined
          }
        >
          <Input
            type="number"
            inputMode="numeric"
            min={1}
            max={PROXY_LIMITS.maxConcurrency}
            placeholder="1"
            value={value.maxConcurrency}
            onChange={(e) => set({ maxConcurrency: e.target.value })}
          />
        </FormField>
        <FormField label={t('import.region')} error={tooLong('region_too_long', PROXY_LIMITS.region)}>
          <Input value={value.region} onChange={(e) => set({ region: e.target.value })} />
        </FormField>
        <FormField label={t('import.city')} error={tooLong('city_too_long', PROXY_LIMITS.city)}>
          <Input value={value.city} onChange={(e) => set({ city: e.target.value })} />
        </FormField>
        <FormField
          label={t('import.provider')}
          error={tooLong('provider_too_long', PROXY_LIMITS.provider)}
          className="sm:col-span-2"
        >
          <Input value={value.provider} onChange={(e) => set({ provider: e.target.value })} />
        </FormField>
        <FormField label={t('import.tags')} description={t('import.tagsHint')} className="sm:col-span-2">
          <TagsInput
            value={value.tags}
            onChange={(tags) => set({ tags })}
            maxTags={PROXY_LIMITS.tags}
            validate={(tag) => tag.length <= PROXY_LIMITS.tag}
            disabled={disabled}
          />
        </FormField>
      </div>
      <SessionTemplateField
        value={value.sessionTemplate}
        onChange={(sessionTemplate) => set({ sessionTemplate })}
        disabled={disabled}
      />
    </fieldset>
  );
}
