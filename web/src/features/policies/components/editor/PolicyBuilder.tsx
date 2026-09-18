import { FileCode2Icon, InfoIcon, RotateCcwIcon, TriangleAlertIcon } from 'lucide-react';
import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';

import { Button } from '@/components/ui/button';

import { type PolicyKind } from '../../constants';
import { loadPolicyModel, policyModelToYaml, type PolicyModels } from '../../model/index';
import { ActionRulesForm } from '../builder/ActionRulesForm';
import { BreakerForm } from '../builder/BreakerForm';
import { RotationForm } from '../builder/RotationForm';
import { SignalRulesForm } from '../builder/SignalRulesForm';

/** A form model together with the YAML it was generated from (or loaded from). */
type Built = { [K in PolicyKind]: { kind: K; model: PolicyModels[K] } }[PolicyKind];

type LoadResult = { ok: true; built: Built } | { ok: false; error: string };

function load(kind: PolicyKind, text: string): LoadResult {
  switch (kind) {
    case 'rotation': {
      const r = loadPolicyModel('rotation', text);
      return r.ok ? { ok: true, built: { kind, model: r.model } } : r;
    }
    case 'signal': {
      const r = loadPolicyModel('signal', text);
      return r.ok ? { ok: true, built: { kind, model: r.model } } : r;
    }
    case 'action': {
      const r = loadPolicyModel('action', text);
      return r.ok ? { ok: true, built: { kind, model: r.model } } : r;
    }
    case 'breaker': {
      const r = loadPolicyModel('breaker', text);
      return r.ok ? { ok: true, built: { kind, model: r.model } } : r;
    }
  }
}

function toYaml(built: Built): string {
  switch (built.kind) {
    case 'rotation':
      return policyModelToYaml('rotation', built.model);
    case 'signal':
      return policyModelToYaml('signal', built.model);
    case 'action':
      return policyModelToYaml('action', built.model);
    case 'breaker':
      return policyModelToYaml('breaker', built.model);
  }
}

export interface PolicyBuilderProps {
  kind: PolicyKind;
  text: string;
  onTextChange: (text: string) => void;
  onOpenYaml: () => void;
  /** Replaces the YAML with the kind template (when the YAML cannot be loaded). */
  onResetTemplate: () => void;
  disabled?: boolean;
}

/**
 * Form and rule-list modes. The YAML text stays the source of truth: the form
 * is loaded from it with the feature's YAML subset parser and every form edit
 * regenerates the YAML. The last built model is kept so list item identity
 * (expanded rules) survives regeneration.
 */
export function PolicyBuilder({
  kind,
  text,
  onTextChange,
  onOpenYaml,
  onResetTemplate,
  disabled,
}: PolicyBuilderProps) {
  const { t } = useTranslation('policies');
  const [last, setLast] = useState<{ text: string; built: Built } | null>(null);
  const current = useMemo<LoadResult>(
    () =>
      last && last.text === text && last.built.kind === kind
        ? { ok: true, built: last.built }
        : load(kind, text),
    [last, text, kind],
  );

  if (!current.ok) {
    return (
      <div
        role="alert"
        className="flex flex-col items-center gap-3 rounded-lg border border-dashed px-6 py-12 text-center"
      >
        <div className="flex size-10 items-center justify-center rounded-full bg-amber-500/10 text-amber-600">
          <TriangleAlertIcon className="size-5" aria-hidden />
        </div>
        <p className="text-sm font-medium">{t('builder.loadFailedTitle')}</p>
        <p className="max-w-lg text-sm text-muted-foreground">{t('builder.loadFailedDescription')}</p>
        <code className="max-w-lg rounded bg-muted px-2 py-1 font-mono text-xs break-words">
          {current.error}
        </code>
        <div className="flex flex-wrap justify-center gap-2">
          <Button variant="outline" size="sm" onClick={onOpenYaml}>
            <FileCode2Icon />
            {t('editor.openYaml')}
          </Button>
          <Button variant="ghost" size="sm" onClick={onResetTemplate} disabled={disabled}>
            <RotateCcwIcon />
            {t('builder.resetTemplate')}
          </Button>
        </div>
      </div>
    );
  }

  const commit = (built: Built) => {
    const yaml = toYaml(built);
    setLast({ text: yaml, built });
    onTextChange(yaml);
  };

  const built = current.built;
  let form;
  switch (built.kind) {
    case 'rotation':
      form = (
        <RotationForm
          model={built.model}
          onChange={(model) => commit({ kind: 'rotation', model })}
          disabled={disabled}
        />
      );
      break;
    case 'breaker':
      form = (
        <BreakerForm
          model={built.model}
          onChange={(model) => commit({ kind: 'breaker', model })}
          disabled={disabled}
        />
      );
      break;
    case 'signal':
      form = (
        <SignalRulesForm
          model={built.model}
          onChange={(model) => commit({ kind: 'signal', model })}
          disabled={disabled}
        />
      );
      break;
    case 'action':
      form = (
        <ActionRulesForm
          model={built.model}
          onChange={(model) => commit({ kind: 'action', model })}
          disabled={disabled}
        />
      );
      break;
  }

  return (
    <div className="grid gap-3">
      <p className="flex items-start gap-2 rounded-md border bg-muted/40 px-3 py-2 text-xs text-muted-foreground">
        <InfoIcon className="mt-0.5 size-3.5 shrink-0" aria-hidden />
        {t('builder.regenerateNotice')}
      </p>
      {form}
    </div>
  );
}
