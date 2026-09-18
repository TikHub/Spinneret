import { useTranslation } from 'react-i18next';

import { ACTIONS, NAME_PATTERN, OUTCOMES, scopesForAction } from '../../constants';
import { ACTION_DEFAULTS, withAction, type ActionRuleModel } from '../../model/action';
import {
  CheckboxGroup,
  DurationField,
  FieldSection,
  NumberField,
  SelectField,
  SwitchField,
  TextField,
} from '../fields';
import { useEnumOptions } from '../useEnumOptions';

export interface ActionRuleEditorProps {
  rule: ActionRuleModel;
  rules: readonly ActionRuleModel[];
  onChange: (rule: ActionRuleModel) => void;
  disabled?: boolean;
}

/** Rule names reserved for built-in health and lifecycle actions. */
const RESERVED_NAMES = ['lifecycle.activate', 'health.endpoint_low', 'health.quarantine'];

/** Editor of one action rule; the scope list follows the selected action. */
export function ActionRuleEditor({ rule, rules, onChange, disabled }: ActionRuleEditorProps) {
  const { t } = useTranslation('policies');
  const actions = useEnumOptions(ACTIONS, 'actionKinds');
  const scopes = useEnumOptions(scopesForAction(rule.action), 'scopes');
  const outcomeOptions = OUTCOMES.map((o) => ({
    value: o,
    label: <span className="font-mono text-xs">{o}</span>,
  }));
  const set = <K extends keyof ActionRuleModel>(key: K, value: ActionRuleModel[K]) =>
    onChange({ ...rule, [key]: value });

  let nameError: string | undefined;
  if (rule.name !== '' && !NAME_PATTERN.test(rule.name)) nameError = t('rules.nameErrors.pattern');
  else if (rule.name !== '' && rules.some((r) => r.key !== rule.key && r.name === rule.name)) {
    nameError = t('rules.nameErrors.duplicate');
  } else if (RESERVED_NAMES.includes(rule.name)) nameError = t('rules.nameErrors.reserved');

  return (
    <div className="grid gap-4">
      <FieldSection title={t('rules.basics')} className="border-dashed bg-transparent">
        <TextField
          label={t('fields.ruleName')}
          description={t('rules.nameHint')}
          value={rule.name}
          onChange={(name) => set('name', name)}
          error={nameError}
          mono
          maxLength={64}
          disabled={disabled}
        />
        <SelectField
          label={t('fields.action')}
          value={rule.action}
          onChange={(action) => onChange(withAction(rule, action))}
          options={actions}
          required
          disabled={disabled}
        />
        <SelectField
          label={t('fields.scope')}
          description={t('action.scopeHint')}
          value={rule.scope}
          onChange={(scope) => set('scope', scope)}
          options={scopes}
          required
          disabled={disabled}
        />
        <CheckboxGroup
          label={t('fields.outcomes')}
          value={rule.outcomes}
          onChange={(outcomes) => set('outcomes', outcomes)}
          options={outcomeOptions}
          error={rule.outcomes.length === 0 ? t('action.outcomesRequired') : undefined}
          disabled={disabled}
          className="sm:col-span-2 xl:col-span-3"
        />
      </FieldSection>

      <FieldSection
        title={t('action.countTitle')}
        description={t('action.countHint')}
        className="border-dashed bg-transparent"
      >
        <SwitchField
          label={t('fields.countEnabled')}
          checked={rule.countEnabled}
          onChange={(countEnabled) => onChange({ ...rule, countEnabled })}
          disabled={disabled}
        />
        {rule.countEnabled && (
          <>
            <NumberField
              label={t('fields.countGte')}
              integer
              required
              value={rule.countGte}
              onChange={(v) => set('countGte', v)}
              placeholder="3"
              disabled={disabled}
            />
            <DurationField
              label={t('fields.countWithin')}
              required
              value={rule.countWithin}
              onChange={(v) => set('countWithin', v)}
              placeholder="10m"
              disabled={disabled}
            />
          </>
        )}
      </FieldSection>

      {rule.action === 'cooldown' && (
        <FieldSection
          title={t('action.cooldownTitle')}
          description={t('action.cooldownHint')}
          className="border-dashed bg-transparent"
        >
          <DurationField
            label={t('fields.base')}
            required
            allowEmpty={false}
            value={rule.base}
            onChange={(v) => set('base', v)}
            placeholder="60s"
            disabled={disabled}
          />
          <NumberField
            label={t('fields.multiplier')}
            value={rule.multiplier}
            onChange={(v) => set('multiplier', v)}
            placeholder={ACTION_DEFAULTS.multiplier}
            disabled={disabled}
          />
          <DurationField
            label={t('fields.max')}
            value={rule.max}
            onChange={(v) => set('max', v)}
            placeholder={ACTION_DEFAULTS.max}
            disabled={disabled}
          />
          <NumberField
            label={t('fields.maxExponent')}
            integer
            value={rule.maxExponent}
            onChange={(v) => set('maxExponent', v)}
            placeholder={ACTION_DEFAULTS.maxExponent}
            disabled={disabled}
          />
          <DurationField
            label={t('fields.failureResetAfter')}
            value={rule.failureResetAfter}
            onChange={(v) => set('failureResetAfter', v)}
            placeholder={ACTION_DEFAULTS.failureResetAfter}
            disabled={disabled}
          />
        </FieldSection>
      )}

      {(rule.action === 'ban' || rule.action === 'quarantine') && (
        <FieldSection title={t('action.durationTitle')} className="border-dashed bg-transparent">
          <DurationField
            label={t('fields.duration')}
            description={
              rule.action === 'ban' ? t('action.banDurationHint') : t('action.quarantineDurationHint')
            }
            required
            allowEmpty={false}
            allowPermanent={rule.action === 'ban'}
            value={rule.duration}
            onChange={(v) => set('duration', v)}
            placeholder={rule.action === 'ban' ? '12h' : '24h'}
            disabled={disabled}
          />
        </FieldSection>
      )}
      {rule.action === 'expire' && <p className="text-xs text-muted-foreground">{t('action.expireHint')}</p>}
    </div>
  );
}
