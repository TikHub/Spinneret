import { PlusIcon, Trash2Icon } from 'lucide-react';
import { useId } from 'react';
import { useTranslation } from 'react-i18next';

import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { DurationInput } from '@/components/DurationInput';

import { PROXY_KINDS, PROXY_MODES, REUSE_ANCHORS, REUSE_SCOPES, ROTATION_STRATEGIES } from '../../constants';
import { newKey } from '../../model/common';
import { ROTATION_DEFAULTS, type RotationModel } from '../../model/rotation';
import {
  CheckboxGroup,
  DurationField,
  FieldSection,
  NumberField,
  SelectField,
  SwitchField,
  TagsField,
} from '../fields';
import { useEnumOptions } from '../useEnumOptions';
import { MetaFields } from './MetaFields';

export interface RotationFormProps {
  model: RotationModel;
  onChange: (model: RotationModel) => void;
  disabled?: boolean;
}

/** Form mode of a rotation policy. */
export function RotationForm({ model, onChange, disabled }: RotationFormProps) {
  const { t } = useTranslation('policies');
  const strategies = useEnumOptions(ROTATION_STRATEGIES, 'strategies', false);
  const anchors = useEnumOptions(REUSE_ANCHORS, 'reuseAnchors', false);
  const scopes = useEnumOptions(REUSE_SCOPES, 'reuseScopes', false);
  const proxyModes = useEnumOptions(PROXY_MODES, 'proxyModes', false);
  const proxyKinds = useEnumOptions(PROXY_KINDS, 'proxyKinds', false);
  const set = <K extends keyof RotationModel>(key: K, value: RotationModel[K]) =>
    onChange({ ...model, [key]: value });
  const setProxy = <K extends keyof RotationModel['proxy']>(key: K, value: RotationModel['proxy'][K]) =>
    onChange({ ...model, proxy: { ...model.proxy, [key]: value } });
  const d = ROTATION_DEFAULTS;

  return (
    <div className="grid gap-4">
      <MetaFields model={model} onChange={onChange} disabled={disabled}>
        <TagsField
          label={t('fields.identityTypes')}
          description={t('builder.identityTypesHint')}
          value={model.identityTypes}
          onChange={(v) => set('identityTypes', v)}
          disabled={disabled}
          className="sm:col-span-2 xl:col-span-3"
        />
      </MetaFields>

      <FieldSection title={t('builder.sections.selection')}>
        <SelectField
          label={t('fields.strategy')}
          value={model.strategy}
          onChange={(v) => set('strategy', v)}
          options={strategies}
          disabled={disabled}
        />
        <NumberField
          label={t('fields.candidateSample')}
          integer
          value={model.candidateSample}
          onChange={(v) => set('candidateSample', v)}
          placeholder={d.candidateSample}
          disabled={disabled}
          description={t('builder.range', { min: 1, max: 256 })}
        />
        <NumberField
          label={t('fields.maxConcurrentLeases')}
          integer
          value={model.maxConcurrentLeases}
          onChange={(v) => set('maxConcurrentLeases', v)}
          placeholder={d.maxConcurrentLeases}
          disabled={disabled}
          description={t('builder.maxConcurrentLeasesHint')}
        />
      </FieldSection>

      <FieldSection title={t('builder.sections.leases')}>
        <DurationField
          label={t('fields.leaseTtl')}
          value={model.leaseTtl}
          onChange={(v) => set('leaseTtl', v)}
          placeholder={d.leaseTtl}
          disabled={disabled}
          description={t('builder.leaseTtlHint')}
        />
        <DurationField
          label={t('fields.maxLeaseLifetime')}
          value={model.maxLeaseLifetime}
          onChange={(v) => set('maxLeaseLifetime', v)}
          placeholder={d.maxLeaseLifetime}
          disabled={disabled}
        />
        <DurationField
          label={t('fields.reuseInterval')}
          value={model.reuseInterval}
          onChange={(v) => set('reuseInterval', v)}
          placeholder={d.reuseInterval}
          disabled={disabled}
        />
        <SelectField
          label={t('fields.reuseAnchor')}
          value={model.reuseAnchor}
          onChange={(v) => set('reuseAnchor', v)}
          options={anchors}
          disabled={disabled}
        />
        <SelectField
          label={t('fields.reuseScope')}
          value={model.reuseScope}
          onChange={(v) => set('reuseScope', v)}
          options={scopes}
          disabled={disabled}
        />
      </FieldSection>

      <QuotaEditor model={model} onChange={onChange} disabled={disabled} />

      <FieldSection title={t('builder.sections.stickyWarmupProbe')}>
        <SwitchField
          label={t('fields.stickyEnabled')}
          checked={model.sticky.enabled}
          onChange={(enabled) => set('sticky', { ...model.sticky, enabled })}
          disabled={disabled}
        />
        <DurationField
          label={t('fields.stickyTtl')}
          value={model.sticky.ttl}
          onChange={(ttl) => set('sticky', { ...model.sticky, ttl })}
          placeholder={d.stickyTtl}
          disabled={disabled}
        />
        <div className="hidden xl:block" />
        <DurationField
          label={t('fields.warmupDuration')}
          value={model.warmup.duration}
          onChange={(duration) => set('warmup', { ...model.warmup, duration })}
          placeholder={d.warmupDuration}
          disabled={disabled}
        />
        <NumberField
          label={t('fields.warmupQuotaFactor')}
          value={model.warmup.quotaFactor}
          onChange={(quotaFactor) => set('warmup', { ...model.warmup, quotaFactor })}
          placeholder={d.warmupQuotaFactor}
          disabled={disabled}
        />
        <div className="hidden xl:block" />
        <NumberField
          label={t('fields.probeWeightFactor')}
          value={model.probe.weightFactor}
          onChange={(weightFactor) => set('probe', { ...model.probe, weightFactor })}
          placeholder={d.probeWeightFactor}
          disabled={disabled}
        />
        <NumberField
          label={t('fields.probeMaxLeases')}
          integer
          value={model.probe.maxLeases}
          onChange={(maxLeases) => set('probe', { ...model.probe, maxLeases })}
          placeholder={d.probeMaxLeases}
          disabled={disabled}
        />
      </FieldSection>

      <FieldSection title={t('builder.sections.proxy')}>
        <SelectField
          label={t('fields.proxyMode')}
          value={model.proxy.mode}
          onChange={(v) => setProxy('mode', v)}
          options={proxyModes}
          disabled={disabled}
        />
        <DurationField
          label={t('fields.rebindTolerance')}
          value={model.proxy.rebindTolerance}
          onChange={(v) => setProxy('rebindTolerance', v)}
          placeholder={d.rebindTolerance}
          disabled={disabled}
        />
        <NumberField
          label={t('fields.maxRebindsPerDay')}
          integer
          value={model.proxy.maxRebindsPerDay}
          onChange={(v) => setProxy('maxRebindsPerDay', v)}
          placeholder={d.maxRebindsPerDay}
          disabled={disabled}
        />
        <CheckboxGroup
          label={t('fields.proxyKinds')}
          value={model.proxy.kinds}
          onChange={(v) => setProxy('kinds', v)}
          options={proxyKinds}
          disabled={disabled}
          className="sm:col-span-2"
        />
        <SwitchField
          label={t('fields.regionMatch')}
          checked={model.proxy.regionMatch}
          onChange={(v) => setProxy('regionMatch', v)}
          disabled={disabled}
        />
        <TagsField
          label={t('fields.proxyTags')}
          value={model.proxy.tags}
          onChange={(v) => setProxy('tags', v)}
          disabled={disabled}
        />
        <TagsField
          label={t('fields.proxyProviders')}
          value={model.proxy.providers}
          onChange={(v) => setProxy('providers', v)}
          disabled={disabled}
        />
        <TagsField
          label={t('fields.proxyRegions')}
          value={model.proxy.regions}
          onChange={(v) => setProxy('regions', v)}
          disabled={disabled}
        />
      </FieldSection>
    </div>
  );
}

function QuotaEditor({ model, onChange, disabled }: RotationFormProps) {
  const { t } = useTranslation('policies');
  const update = (key: string, patch: Partial<RotationModel['quota'][number]>) =>
    onChange({ ...model, quota: model.quota.map((q) => (q.key === key ? { ...q, ...patch } : q)) });
  return (
    <section className="rounded-lg border bg-card p-4">
      <div className="mb-3 flex items-center justify-between gap-2">
        <div className="space-y-0.5">
          <h3 className="text-sm font-semibold">{t('builder.sections.quota')}</h3>
          <p className="text-xs text-muted-foreground">{t('builder.quotaHint')}</p>
        </div>
        <Button
          variant="outline"
          size="sm"
          disabled={disabled}
          onClick={() =>
            onChange({ ...model, quota: [...model.quota, { key: newKey(), limit: '', window: '1h' }] })
          }
        >
          <PlusIcon />
          {t('builder.addQuota')}
        </Button>
      </div>
      {model.quota.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t('builder.noQuota')}</p>
      ) : (
        <ul className="grid gap-2">
          {model.quota.map((q, index) => (
            <QuotaRow
              key={q.key}
              index={index}
              limit={q.limit}
              window={q.window}
              disabled={disabled}
              onChange={(patch) => update(q.key, patch)}
              onRemove={() => onChange({ ...model, quota: model.quota.filter((x) => x.key !== q.key) })}
            />
          ))}
        </ul>
      )}
    </section>
  );
}

interface QuotaRowProps {
  index: number;
  limit: string;
  window: string;
  disabled?: boolean;
  onChange: (patch: { limit?: string; window?: string }) => void;
  onRemove: () => void;
}

function QuotaRow({ index, limit, window, disabled, onChange, onRemove }: QuotaRowProps) {
  const { t } = useTranslation('policies');
  const windowId = useId();
  return (
    <li className="grid grid-cols-[minmax(6rem,10rem)_minmax(8rem,14rem)_auto] items-start gap-2">
      <Input
        value={limit}
        onChange={(e) => onChange({ limit: e.target.value })}
        placeholder={t('fields.quotaLimit')}
        aria-label={t('builder.quotaLimitLabel', { index: index + 1 })}
        inputMode="numeric"
        className="font-mono"
        disabled={disabled}
      />
      <div>
        <label htmlFor={windowId} className="sr-only">
          {t('builder.quotaWindowLabel', { index: index + 1 })}
        </label>
        <DurationInput
          id={windowId}
          value={window}
          onChange={(w) => onChange({ window: w })}
          placeholder={t('fields.quotaWindow')}
          disabled={disabled}
        />
      </div>
      <Button
        variant="ghost"
        size="icon"
        disabled={disabled}
        aria-label={t('builder.removeQuota', { index: index + 1 })}
        onClick={onRemove}
      >
        <Trash2Icon />
      </Button>
    </li>
  );
}
