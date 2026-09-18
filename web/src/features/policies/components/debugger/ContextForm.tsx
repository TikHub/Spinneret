import { useTranslation } from 'react-i18next';

import { IDENTITY_STATES } from '../../constants';
import { type DebugContextFields, type DebugFieldError } from '../../debug';
import { DurationField, FieldSection, NumberField, SelectField, SwitchField } from '../fields';

export interface ContextFormProps {
  value: DebugContextFields;
  onChange: (value: DebugContextFields) => void;
  errors: Record<string, DebugFieldError>;
}

/** Assumed hot state after the report: identity state, scores, samples and failure streaks. */
export function ContextForm({ value, onChange, errors }: ContextFormProps) {
  const { t } = useTranslation('policies');
  const set = <K extends keyof DebugContextFields>(key: K, v: DebugContextFields[K]) =>
    onChange({ ...value, [key]: v });
  const err = (field: keyof DebugContextFields) => {
    const e = errors[`context.${field}`];
    return e ? t(`debugger.errors.${e}`) : undefined;
  };
  const states = IDENTITY_STATES.map((s) => ({ value: s, label: t(`common:states.${s}`) }));

  return (
    <FieldSection title={t('debugger.contextTitle')} description={t('debugger.contextHint')}>
      <SelectField
        label={t('fields.identityState')}
        value={value.identityState}
        onChange={(v) => set('identityState', v)}
        options={states}
      />
      <SwitchField
        label={t('fields.hasAccount')}
        checked={value.hasAccount}
        onChange={(v) => set('hasAccount', v)}
      />
      <SwitchField
        label={t('fields.hasProxy')}
        checked={value.hasProxy}
        onChange={(v) => set('hasProxy', v)}
      />
      <NumberField
        label={t('fields.endpointScore')}
        value={value.endpointScore}
        onChange={(v) => set('endpointScore', v)}
        description={t('debugger.scoreHint')}
        error={err('endpointScore')}
      />
      <NumberField
        label={t('fields.endpointSamples')}
        integer
        value={value.endpointSamples}
        onChange={(v) => set('endpointSamples', v)}
        error={err('endpointSamples')}
      />
      <DurationField
        label={t('fields.endpointCooldownRemaining')}
        value={value.endpointCooldownRemaining}
        onChange={(v) => set('endpointCooldownRemaining', v)}
        description={t('debugger.cooldownRemainingHint')}
        error={err('endpointCooldownRemaining')}
      />
      <NumberField
        label={t('fields.globalScore')}
        value={value.globalScore}
        onChange={(v) => set('globalScore', v)}
        description={t('debugger.scoreHint')}
        error={err('globalScore')}
      />
      <NumberField
        label={t('fields.globalSamples')}
        integer
        value={value.globalSamples}
        onChange={(v) => set('globalSamples', v)}
        error={err('globalSamples')}
      />
      <div className="hidden xl:block" />
      <NumberField
        label={t('fields.endpointStreak')}
        integer
        value={value.endpointStreak}
        onChange={(v) => set('endpointStreak', v)}
        description={t('debugger.streakHint')}
        error={err('endpointStreak')}
      />
      <NumberField
        label={t('fields.siteStreak')}
        integer
        value={value.siteStreak}
        onChange={(v) => set('siteStreak', v)}
        description={t('debugger.siteStreakHint')}
        error={err('siteStreak')}
      />
      <NumberField
        label={t('fields.proxyStreak')}
        integer
        value={value.proxyStreak}
        onChange={(v) => set('proxyStreak', v)}
        error={err('proxyStreak')}
      />
    </FieldSection>
  );
}
