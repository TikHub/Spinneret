import { useTranslation } from 'react-i18next';

import { REVERT_MODES, type RevertMode } from '../../constants';
import { BREAKER_DEFAULTS, type BreakerModel } from '../../model/breaker';
import { DurationField, FieldSection, NumberField, SelectField, SwitchField } from '../fields';
import { useEnumOptions } from '../useEnumOptions';
import { MetaFields } from './MetaFields';

export interface BreakerFormProps {
  model: BreakerModel;
  onChange: (model: BreakerModel) => void;
  disabled?: boolean;
}

/** Form mode of a breaker policy. */
export function BreakerForm({ model, onChange, disabled }: BreakerFormProps) {
  const { t } = useTranslation('policies');
  const revertModes = useEnumOptions(REVERT_MODES, 'revertModes', false);
  const set = <K extends keyof BreakerModel>(key: K, value: BreakerModel[K]) =>
    onChange({ ...model, [key]: value });
  const setTrip = (patch: Partial<BreakerModel['trip']>) => set('trip', { ...model.trip, ...patch });
  const setHalfOpen = (patch: Partial<BreakerModel['halfOpen']>) =>
    set('halfOpen', { ...model.halfOpen, ...patch });
  const d = BREAKER_DEFAULTS;

  return (
    <div className="grid gap-4">
      <MetaFields model={model} onChange={onChange} disabled={disabled}>
        <SwitchField
          label={t('fields.breakerEnabled')}
          description={t('builder.breakerEnabledHint')}
          checked={model.enabled}
          onChange={(v) => set('enabled', v)}
          disabled={disabled}
        />
      </MetaFields>

      <FieldSection title={t('builder.sections.window')} description={t('builder.windowHint')}>
        <DurationField
          label={t('fields.window')}
          value={model.window}
          onChange={(v) => set('window', v)}
          placeholder={d.window}
          disabled={disabled}
        />
        <NumberField
          label={t('fields.buckets')}
          integer
          value={model.buckets}
          onChange={(v) => set('buckets', v)}
          placeholder={d.buckets}
          disabled={disabled}
          description={t('builder.range', { min: 1, max: 60 })}
        />
        <NumberField
          label={t('fields.minRequests')}
          integer
          value={model.minRequests}
          onChange={(v) => set('minRequests', v)}
          placeholder={d.minRequests}
          disabled={disabled}
        />
      </FieldSection>

      <FieldSection title={t('builder.sections.trip')} description={t('builder.tripHint')}>
        <NumberField
          label={t('fields.riskRatioGte')}
          value={model.trip.riskRatioGte}
          onChange={(v) => setTrip({ riskRatioGte: v })}
          placeholder={d.riskRatioGte}
          disabled={disabled}
          description={t('builder.ratioHint')}
        />
        <NumberField
          label={t('fields.distinctCaptchaIdentitiesGte')}
          integer
          value={model.trip.distinctCaptchaIdentitiesGte}
          onChange={(v) => setTrip({ distinctCaptchaIdentitiesGte: v })}
          placeholder={d.distinctCaptchaIdentitiesGte}
          disabled={disabled}
        />
        <NumberField
          label={t('fields.successRatioLte')}
          value={model.trip.successRatioLte}
          onChange={(v) => setTrip({ successRatioLte: v })}
          placeholder={d.successRatioLte}
          disabled={disabled}
          description={t('builder.ratioHint')}
        />
      </FieldSection>

      <FieldSection title={t('builder.sections.open')} description={t('builder.openHint')}>
        <DurationField
          label={t('fields.openDuration')}
          value={model.openDuration}
          onChange={(v) => set('openDuration', v)}
          placeholder={d.openDuration}
          disabled={disabled}
        />
        <DurationField
          label={t('fields.maxOpenDuration')}
          value={model.maxOpenDuration}
          onChange={(v) => set('maxOpenDuration', v)}
          placeholder={d.maxOpenDuration}
          disabled={disabled}
        />
        <DurationField
          label={t('fields.resetOpenCountAfter')}
          value={model.resetOpenCountAfter}
          onChange={(v) => set('resetOpenCountAfter', v)}
          placeholder={d.resetOpenCountAfter}
          disabled={disabled}
        />
      </FieldSection>

      <FieldSection title={t('builder.sections.halfOpen')} description={t('builder.halfOpenHint')}>
        <NumberField
          label={t('fields.probeLeasesPer10s')}
          integer
          value={model.halfOpen.probeLeasesPer10s}
          onChange={(v) => setHalfOpen({ probeLeasesPer10s: v })}
          placeholder={d.probeLeasesPer10s}
          disabled={disabled}
        />
        <NumberField
          label={t('fields.closeMinSamples')}
          integer
          value={model.halfOpen.closeMinSamples}
          onChange={(v) => setHalfOpen({ closeMinSamples: v })}
          placeholder={d.closeMinSamples}
          disabled={disabled}
        />
        <NumberField
          label={t('fields.closeSuccessRatioGte')}
          value={model.halfOpen.closeSuccessRatioGte}
          onChange={(v) => setHalfOpen({ closeSuccessRatioGte: v })}
          placeholder={d.closeSuccessRatioGte}
          disabled={disabled}
        />
      </FieldSection>

      <FieldSection title={t('builder.sections.revert')}>
        <SelectField
          label={t('fields.revertRecentCooldowns')}
          description={t(`revertModeHints.${model.revertRecentCooldowns}`)}
          value={model.revertRecentCooldowns}
          onChange={(v) => set('revertRecentCooldowns', v as RevertMode)}
          options={revertModes}
          disabled={disabled}
        />
      </FieldSection>
    </div>
  );
}
