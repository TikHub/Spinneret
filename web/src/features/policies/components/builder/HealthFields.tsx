import { PlusIcon, Trash2Icon } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';

import { OUTCOMES } from '../../constants';
import { ACTION_DEFAULTS, type ActionModel, type HealthModel } from '../../model/action';
import { newKey } from '../../model/common';
import { DurationField, FieldSection, NumberField, SwitchField } from '../fields';

export interface HealthFieldsProps {
  model: ActionModel;
  onChange: (model: ActionModel) => void;
  disabled?: boolean;
}

/** Health score thresholds, observation overrides and cross attribution of an action policy. */
export function HealthFields({ model, onChange, disabled }: HealthFieldsProps) {
  const { t } = useTranslation('policies');
  const h = model.health;
  const c = model.crossAttribution;
  const d = ACTION_DEFAULTS;
  const setHealth = (patch: Partial<HealthModel>) => onChange({ ...model, health: { ...h, ...patch } });
  const setCross = (patch: Partial<ActionModel['crossAttribution']>) =>
    onChange({ ...model, crossAttribution: { ...c, ...patch } });

  return (
    <>
      <FieldSection title={t('action.healthTitle')} description={t('action.healthHint')}>
        <NumberField
          label={t('fields.alpha')}
          value={h.alpha}
          onChange={(alpha) => setHealth({ alpha })}
          placeholder={d.alpha}
          disabled={disabled}
        />
        <NumberField
          label={t('fields.baseline')}
          value={h.baseline}
          onChange={(baseline) => setHealth({ baseline })}
          placeholder={d.baseline}
          disabled={disabled}
        />
        <DurationField
          label={t('fields.tau')}
          value={h.tau}
          onChange={(tau) => setHealth({ tau })}
          placeholder={d.tau}
          disabled={disabled}
        />
        <NumberField
          label={t('fields.endpointLowScore')}
          value={h.endpointLowScore}
          onChange={(endpointLowScore) => setHealth({ endpointLowScore })}
          placeholder={d.endpointLowScore}
          disabled={disabled}
        />
        <NumberField
          label={t('fields.endpointLowMinSamples')}
          integer
          value={h.endpointLowMinSamples}
          onChange={(endpointLowMinSamples) => setHealth({ endpointLowMinSamples })}
          placeholder={d.endpointLowMinSamples}
          disabled={disabled}
        />
        <DurationField
          label={t('fields.endpointLowCooldown')}
          value={h.endpointLowCooldown}
          onChange={(endpointLowCooldown) => setHealth({ endpointLowCooldown })}
          placeholder={d.endpointLowCooldown}
          disabled={disabled}
        />
        <NumberField
          label={t('fields.quarantineScore')}
          value={h.quarantineScore}
          onChange={(quarantineScore) => setHealth({ quarantineScore })}
          placeholder={d.quarantineScore}
          disabled={disabled}
        />
        <NumberField
          label={t('fields.quarantineMinSamples')}
          integer
          value={h.quarantineMinSamples}
          onChange={(quarantineMinSamples) => setHealth({ quarantineMinSamples })}
          placeholder={d.quarantineMinSamples}
          disabled={disabled}
        />
        <DurationField
          label={t('fields.quarantineDuration')}
          value={h.quarantineDuration}
          onChange={(quarantineDuration) => setHealth({ quarantineDuration })}
          placeholder={d.quarantineDuration}
          disabled={disabled}
        />
        <ObservationsEditor health={h} onChange={setHealth} disabled={disabled} />
      </FieldSection>

      <FieldSection title={t('action.crossTitle')} description={t('action.crossHint')}>
        <SwitchField
          label={t('fields.crossEnabled')}
          checked={c.enabled}
          onChange={(enabled) => setCross({ enabled })}
          disabled={disabled}
        />
        <DurationField
          label={t('fields.crossWindow')}
          value={c.window}
          onChange={(window) => setCross({ window })}
          placeholder={d.crossWindow}
          disabled={disabled}
        />
        <div className="hidden xl:block" />
        <NumberField
          label={t('fields.proxyDistinctIdentities')}
          integer
          value={c.proxyDistinctIdentities}
          onChange={(proxyDistinctIdentities) => setCross({ proxyDistinctIdentities })}
          placeholder={d.proxyDistinctIdentities}
          disabled={disabled}
        />
        <NumberField
          label={t('fields.identityDistinctProxies')}
          integer
          value={c.identityDistinctProxies}
          onChange={(identityDistinctProxies) => setCross({ identityDistinctProxies })}
          placeholder={d.identityDistinctProxies}
          disabled={disabled}
        />
      </FieldSection>
    </>
  );
}

interface ObservationsEditorProps {
  health: HealthModel;
  onChange: (patch: Partial<HealthModel>) => void;
  disabled?: boolean;
}

function ObservationsEditor({ health, onChange, disabled }: ObservationsEditorProps) {
  const { t } = useTranslation('policies');
  const rows = health.observations;
  const used = new Set(rows.map((r) => r.outcome));
  const update = (key: string, patch: { outcome?: string; value?: string }) =>
    onChange({ observations: rows.map((r) => (r.key === key ? { ...r, ...patch } : r)) });

  return (
    <div className="grid gap-2 sm:col-span-2 xl:col-span-3">
      <div className="space-y-0.5">
        <span className="text-sm font-medium">{t('fields.observations')}</span>
        <p className="text-xs text-muted-foreground">{t('action.observationsHint')}</p>
      </div>
      {rows.map((row, index) => (
        <div key={row.key} className="grid grid-cols-[minmax(10rem,14rem)_8rem_auto] items-center gap-2">
          <Select
            value={row.outcome}
            onValueChange={(outcome) => update(row.key, { outcome })}
            disabled={disabled}
          >
            <SelectTrigger
              className="w-full font-mono"
              aria-label={t('action.observationOutcomeLabel', { index: index + 1 })}
            >
              <SelectValue placeholder={t('fields.outcome')} />
            </SelectTrigger>
            <SelectContent>
              {OUTCOMES.map((o) => (
                <SelectItem
                  key={o}
                  value={o}
                  disabled={used.has(o) && o !== row.outcome}
                  className="font-mono"
                >
                  {o}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Input
            value={row.value}
            onChange={(e) => update(row.key, { value: e.target.value })}
            inputMode="decimal"
            placeholder="0..100"
            aria-label={t('action.observationValueLabel', { index: index + 1 })}
            className="font-mono"
            disabled={disabled}
          />
          <Button
            variant="ghost"
            size="icon"
            disabled={disabled}
            aria-label={t('action.removeObservation', { index: index + 1 })}
            onClick={() => onChange({ observations: rows.filter((r) => r.key !== row.key) })}
          >
            <Trash2Icon />
          </Button>
        </div>
      ))}
      <div>
        <Button
          variant="outline"
          size="sm"
          disabled={disabled || used.size >= OUTCOMES.length}
          onClick={() => {
            const outcome = OUTCOMES.find((o) => !used.has(o)) ?? '';
            onChange({ observations: [...rows, { key: newKey(), outcome, value: '' }] });
          }}
        >
          <PlusIcon />
          {t('action.addObservation')}
        </Button>
      </div>
    </div>
  );
}
