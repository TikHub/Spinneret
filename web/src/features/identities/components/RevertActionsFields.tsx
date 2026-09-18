import { useTranslation } from 'react-i18next';

import { Button } from '@/components/ui/button';
import { Checkbox } from '@/components/ui/checkbox';
import { FormField } from '@/components/ui/form';
import { Input } from '@/components/ui/input';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { Switch } from '@/components/ui/switch';

import { REVERTIBLE_ACTIONS, type RevertForm, type RevertFormErrors } from '../revert';
import { toDateTimeLocalValue } from '../timeRange';
import { useActionPolicyOptions } from '../useIdentityOptions';
import { ALL_VALUE, SiteSelect } from './SiteSelect';

const RANGE_PRESETS = [
  ['1h', 3_600_000],
  ['6h', 21_600_000],
  ['24h', 86_400_000],
  ['7d', 604_800_000],
] as const;

export interface RevertActionsFieldsProps {
  form: RevertForm;
  onChange: (form: RevertForm) => void;
  errors: RevertFormErrors;
  disabled?: boolean;
}

function PolicyField({ form, onChange, disabled }: Omit<RevertActionsFieldsProps, 'errors'>) {
  const { t } = useTranslation('identities');
  const policies = useActionPolicyOptions();
  if (!policies.allowed || policies.isError) {
    return (
      <FormField label={t('revert.policy')} description={t('revert.policyIdHint')}>
        <Input
          value={form.policyId}
          maxLength={64}
          disabled={disabled}
          className="font-mono"
          onChange={(e) => onChange({ ...form, policyId: e.target.value })}
        />
      </FormField>
    );
  }
  return (
    <FormField label={t('revert.policy')}>
      <Select
        value={form.policyId || ALL_VALUE}
        onValueChange={(v) => onChange({ ...form, policyId: v === ALL_VALUE ? '' : v })}
        disabled={disabled}
      >
        <SelectTrigger className="w-full">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={ALL_VALUE}>{t('revert.anyPolicy')}</SelectItem>
          {(policies.data ?? []).map((policy) => (
            <SelectItem key={policy.id} value={policy.id}>
              {policy.name}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </FormField>
  );
}

/** Inputs of RevertActions: site, policy, rule, actions, time range and reset flags. */
export function RevertActionsFields({ form, onChange, errors, disabled }: RevertActionsFieldsProps) {
  const { t } = useTranslation('identities');
  const set = (patch: Partial<RevertForm>) => onChange({ ...form, ...patch });

  return (
    <div className="grid gap-3">
      <div className="grid gap-3 sm:grid-cols-3">
        <FormField label={t('fields.site')}>
          <SiteSelect allowAll value={form.site} onChange={(site) => set({ site })} disabled={disabled} />
        </FormField>
        <PolicyField form={form} onChange={onChange} disabled={disabled} />
        <FormField label={t('revert.rule')} error={errors.rule ? t('revert.ruleTooLong') : undefined}>
          <Input
            value={form.rule}
            disabled={disabled}
            placeholder={t('revert.anyRule')}
            onChange={(e) => set({ rule: e.target.value })}
          />
        </FormField>
      </div>
      <fieldset className="grid gap-1.5">
        <legend className="mb-1.5 text-sm font-medium">{t('revert.actions')}</legend>
        <div className="flex flex-wrap gap-4">
          {REVERTIBLE_ACTIONS.map((action) => (
            <FormField key={action} inline label={t(`operations.${action}`)}>
              <Checkbox
                checked={form.actions.includes(action)}
                disabled={disabled}
                onCheckedChange={(checked) =>
                  set({
                    actions: checked ? [...form.actions, action] : form.actions.filter((a) => a !== action),
                  })
                }
              />
            </FormField>
          ))}
        </div>
        <p className="text-xs text-muted-foreground">{t('revert.actionsHint')}</p>
      </fieldset>
      <div className="grid gap-3 sm:grid-cols-2">
        <FormField
          label={t('revert.start')}
          required
          error={
            errors.timeRange === 'startRequired' || errors.timeRange === 'startInvalid'
              ? t('revert.startRequired')
              : errors.timeRange === 'order'
                ? t('revert.rangeOrder')
                : undefined
          }
        >
          <Input
            type="datetime-local"
            value={form.start}
            disabled={disabled}
            onChange={(e) => set({ start: e.target.value })}
          />
        </FormField>
        <FormField
          label={t('revert.end')}
          description={t('revert.endHint')}
          error={errors.timeRange === 'endInvalid' ? t('revert.endInvalid') : undefined}
        >
          <Input
            type="datetime-local"
            value={form.end}
            disabled={disabled}
            onChange={(e) => set({ end: e.target.value })}
          />
        </FormField>
      </div>
      <div className="flex flex-wrap items-center gap-1.5">
        <span className="text-xs text-muted-foreground">{t('revert.quickRange')}</span>
        {RANGE_PRESETS.map(([label, ms]) => (
          <Button
            key={label}
            type="button"
            variant="outline"
            size="sm"
            className="h-7"
            disabled={disabled}
            onClick={() => set({ start: toDateTimeLocalValue(Date.now() - ms), end: '' })}
          >
            {t('revert.last', { range: label })}
          </Button>
        ))}
      </div>
      <div className="flex flex-wrap gap-6 rounded-md border p-3">
        <FormField inline label={t('operation.resetFailures')}>
          <Switch
            checked={form.resetFailures}
            disabled={disabled}
            onCheckedChange={(resetFailures) => set({ resetFailures })}
          />
        </FormField>
        <FormField inline label={t('operation.resetHealth')}>
          <Switch
            checked={form.resetHealth}
            disabled={disabled}
            onCheckedChange={(resetHealth) => set({ resetHealth })}
          />
        </FormField>
      </div>
    </div>
  );
}
