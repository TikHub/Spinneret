import { useTranslation } from 'react-i18next';

import { BLAMES, NAME_PATTERN, OUTCOMES } from '../../constants';
import { newKey } from '../../model/common';
import {
  emptySignalRule,
  signalWhenSummary,
  type SignalModel,
  type SignalRuleModel,
} from '../../model/signal';
import { FieldSection, SelectField, SwitchField, TextField } from '../fields';
import { useEnumOptions } from '../useEnumOptions';
import { MetaFields } from './MetaFields';
import { RuleList } from './RuleList';
import { SignalWhenEditor } from './SignalWhenEditor';

export interface SignalRulesFormProps {
  model: SignalModel;
  onChange: (model: SignalModel) => void;
  disabled?: boolean;
}

function ruleNameError(
  rule: SignalRuleModel,
  rules: readonly SignalRuleModel[],
): 'pattern' | 'duplicate' | undefined {
  if (rule.name === '') return undefined;
  if (!NAME_PATTERN.test(rule.name)) return 'pattern';
  return rules.some((r) => r.key !== rule.key && r.name === rule.name) ? 'duplicate' : undefined;
}

/** Rule-list mode of a signal policy: ordered rules, first match wins. */
export function SignalRulesForm({ model, onChange, disabled }: SignalRulesFormProps) {
  const { t } = useTranslation('policies');
  const outcomes = useEnumOptions(OUTCOMES, 'outcomes');
  const blames = useEnumOptions(BLAMES, 'blames');

  return (
    <div className="grid gap-4">
      <MetaFields
        model={model}
        onChange={onChange}
        disabled={disabled}
        extendsKind="signal"
        extendsValue={model.extends}
        onExtendsChange={(v) => onChange({ ...model, extends: v })}
      >
        <SwitchField
          label={t('fields.trustOutcomeHint')}
          description={t('signal.trustOutcomeHintHint')}
          checked={model.trustOutcomeHint}
          onChange={(v) => onChange({ ...model, trustOutcomeHint: v })}
          disabled={disabled}
        />
      </MetaFields>

      <RuleList<SignalRuleModel>
        title={t('signal.rulesTitle')}
        description={t('signal.rulesHint')}
        items={model.rules}
        onChange={(rules) => onChange({ ...model, rules })}
        createItem={emptySignalRule}
        duplicateItem={(rule) => ({ ...rule, key: newKey(), name: rule.name ? `${rule.name}-copy` : '' })}
        addLabel={t('rules.add')}
        emptyLabel={t('signal.noRules')}
        disabled={disabled}
        renderSummary={(rule) => (
          <span className="flex min-w-0 flex-wrap items-baseline gap-x-2 gap-y-0.5 text-sm">
            <span className="font-medium">{rule.name || t('rules.unnamed')}</span>
            <code className="truncate font-mono text-xs text-muted-foreground">
              {signalWhenSummary(rule.when)}
            </code>
            <span className="text-muted-foreground">→</span>
            <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs">{rule.outcome || '?'}</code>
            {rule.blame && (
              <span className="text-xs text-muted-foreground">
                {t('signal.blameSummary', { blame: rule.blame })}
              </span>
            )}
          </span>
        )}
        renderEditor={(rule, _index, update) => {
          const nameError = ruleNameError(rule, model.rules);
          return (
            <div className="grid gap-4">
              <FieldSection title={t('rules.basics')} className="border-dashed bg-transparent">
                <TextField
                  label={t('fields.ruleName')}
                  description={t('rules.nameHint')}
                  value={rule.name}
                  onChange={(name) => update({ ...rule, name })}
                  error={nameError ? t(`rules.nameErrors.${nameError}`) : undefined}
                  mono
                  maxLength={64}
                  disabled={disabled}
                />
                <SelectField
                  label={t('fields.outcome')}
                  value={rule.outcome}
                  onChange={(outcome) => update({ ...rule, outcome })}
                  options={outcomes}
                  required
                  disabled={disabled}
                />
                <SelectField
                  label={t('fields.blame')}
                  description={t('signal.blameHint')}
                  value={rule.blame}
                  onChange={(blame) => update({ ...rule, blame })}
                  options={blames}
                  noneLabel={t('signal.defaultBlame')}
                  disabled={disabled}
                />
              </FieldSection>
              <SignalWhenEditor
                value={rule.when}
                onChange={(when) => update({ ...rule, when })}
                disabled={disabled}
              />
            </div>
          );
        }}
      />
    </div>
  );
}
