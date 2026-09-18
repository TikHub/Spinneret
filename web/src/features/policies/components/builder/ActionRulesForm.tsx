import { PlusIcon, Trash2Icon } from 'lucide-react';
import { useId } from 'react';
import { useTranslation } from 'react-i18next';

import { DurationInput } from '@/components/DurationInput';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';

import { ACTION_MODES, BAN_EXPIRY_STATES } from '../../constants';
import {
  actionWhenSummary,
  emptyActionRule,
  emptyEscalationStep,
  type ActionModel,
  type ActionRuleModel,
  type EscalationStepModel,
} from '../../model/action';
import { newKey } from '../../model/common';
import { FieldSection, SelectField } from '../fields';
import { useEnumOptions } from '../useEnumOptions';
import { ActionRuleEditor } from './ActionRuleEditor';
import { HealthFields } from './HealthFields';
import { MetaFields } from './MetaFields';
import { RuleList } from './RuleList';

export interface ActionRulesFormProps {
  model: ActionModel;
  onChange: (model: ActionModel) => void;
  disabled?: boolean;
}

/** Rule-list mode of an action policy: all matching rules apply, the most severe action per subject wins. */
export function ActionRulesForm({ model, onChange, disabled }: ActionRulesFormProps) {
  const { t } = useTranslation('policies');
  const modes = useEnumOptions(ACTION_MODES, 'modes', false);
  const banExpiry = useEnumOptions(BAN_EXPIRY_STATES, 'banExpiryStates', false);

  return (
    <div className="grid gap-4">
      <MetaFields
        model={model}
        onChange={onChange}
        disabled={disabled}
        extendsKind="action"
        extendsValue={model.extends}
        onExtendsChange={(v) => onChange({ ...model, extends: v })}
      >
        <SelectField
          label={t('fields.mode')}
          description={t(`modeHints.${model.mode === 'shadow' ? 'shadow' : 'enforce'}`)}
          value={model.mode}
          onChange={(mode) => onChange({ ...model, mode })}
          options={modes}
          disabled={disabled}
        />
        <SelectField
          label={t('fields.banExpiryState')}
          description={t('action.banExpiryHint')}
          value={model.banExpiryState}
          onChange={(banExpiryState) => onChange({ ...model, banExpiryState })}
          options={banExpiry}
          disabled={disabled}
        />
      </MetaFields>

      <RuleList<ActionRuleModel>
        title={t('action.rulesTitle')}
        description={t('action.rulesHint')}
        items={model.rules}
        onChange={(rules) => onChange({ ...model, rules })}
        createItem={emptyActionRule}
        duplicateItem={(rule) => ({ ...rule, key: newKey(), name: rule.name ? `${rule.name}-copy` : '' })}
        addLabel={t('rules.add')}
        emptyLabel={t('action.noRules')}
        disabled={disabled}
        renderSummary={(rule) => (
          <span className="flex min-w-0 flex-wrap items-baseline gap-x-2 gap-y-0.5 text-sm">
            <span className="font-medium">{rule.name || t('rules.unnamed')}</span>
            <code className="truncate font-mono text-xs text-muted-foreground">
              {actionWhenSummary(rule)}
            </code>
            <span className="text-muted-foreground">→</span>
            <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs">
              {rule.action} · {rule.scope}
              {rule.action === 'cooldown' && rule.base && ` · ${rule.base}`}
              {rule.duration && ` · ${rule.duration}`}
            </code>
          </span>
        )}
        renderEditor={(rule, _index, update) => (
          <ActionRuleEditor rule={rule} rules={model.rules} onChange={update} disabled={disabled} />
        )}
      />

      <EscalationEditor
        steps={model.escalation}
        onChange={(escalation) => onChange({ ...model, escalation })}
        disabled={disabled}
      />

      <HealthFields model={model} onChange={onChange} disabled={disabled} />
    </div>
  );
}

interface EscalationEditorProps {
  steps: readonly EscalationStepModel[];
  onChange: (steps: EscalationStepModel[]) => void;
  disabled?: boolean;
}

function EscalationEditor({ steps, onChange, disabled }: EscalationEditorProps) {
  const { t } = useTranslation('policies');
  return (
    <FieldSection title={t('action.escalationTitle')} description={t('action.escalationHint')}>
      <div className="grid gap-2 sm:col-span-2 xl:col-span-3">
        {steps.length === 0 && <p className="text-sm text-muted-foreground">{t('action.noEscalation')}</p>}
        {steps.length > 0 && (
          <div className="hidden grid-cols-[8rem_12rem_12rem_auto] gap-2 text-xs font-medium text-muted-foreground md:grid">
            <span>{t('fields.bansGte')}</span>
            <span>{t('fields.bansWithin')}</span>
            <span>{t('fields.duration')}</span>
          </div>
        )}
        {steps.map((step, index) => (
          <EscalationRow
            key={step.key}
            index={index}
            step={step}
            disabled={disabled}
            onChange={(next) => onChange(steps.map((s) => (s.key === step.key ? next : s)))}
            onRemove={() => onChange(steps.filter((s) => s.key !== step.key))}
          />
        ))}
        <div>
          <Button
            variant="outline"
            size="sm"
            disabled={disabled}
            onClick={() => onChange([...steps, emptyEscalationStep()])}
          >
            <PlusIcon />
            {t('action.addEscalation')}
          </Button>
        </div>
      </div>
    </FieldSection>
  );
}

interface EscalationRowProps {
  index: number;
  step: EscalationStepModel;
  onChange: (step: EscalationStepModel) => void;
  onRemove: () => void;
  disabled?: boolean;
}

function EscalationRow({ index, step, onChange, onRemove, disabled }: EscalationRowProps) {
  const { t } = useTranslation('policies');
  const withinId = useId();
  const durationId = useId();
  const n = index + 1;
  return (
    <div className="grid grid-cols-1 items-start gap-2 md:grid-cols-[8rem_12rem_12rem_auto]">
      <Input
        value={step.gte}
        onChange={(e) => onChange({ ...step, gte: e.target.value })}
        inputMode="numeric"
        placeholder="2"
        aria-label={t('action.escalationGteLabel', { index: n })}
        className="font-mono"
        disabled={disabled}
      />
      <div>
        <label htmlFor={withinId} className="sr-only">
          {t('action.escalationWithinLabel', { index: n })}
        </label>
        <DurationInput
          id={withinId}
          value={step.within}
          onChange={(within) => onChange({ ...step, within })}
          placeholder="7d"
          disabled={disabled}
        />
      </div>
      <div>
        <label htmlFor={durationId} className="sr-only">
          {t('action.escalationDurationLabel', { index: n })}
        </label>
        <DurationInput
          id={durationId}
          value={step.duration}
          onChange={(duration) => onChange({ ...step, duration })}
          allowPermanent
          placeholder="72h"
          disabled={disabled}
        />
      </div>
      <Button
        variant="ghost"
        size="icon"
        onClick={onRemove}
        disabled={disabled}
        aria-label={t('action.removeEscalation', { index: n })}
      >
        <Trash2Icon />
      </Button>
    </div>
  );
}
