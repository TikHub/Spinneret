import { CircleCheckIcon, CircleXIcon, XIcon } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';

import { CodeEditor } from '@/components/editor/CodeEditor';
import { Button } from '@/components/ui/button';
import { type ValidatePolicyResponse } from '@/gen/spinneret/v1/policy_admin_pb';
import { cn } from '@/lib/utils';

export interface ValidationPanelProps {
  policyId: string;
  result: ValidatePolicyResponse;
  /** The YAML has changed since the validation ran. */
  outdated: boolean;
  onClose: () => void;
}

/** Result of ValidatePolicy: the error list, or the normalized YAML (defaults applied). */
export function ValidationPanel({ policyId, result, outdated, onClose }: ValidationPanelProps) {
  const { t } = useTranslation('policies');
  const [showNormalized, setShowNormalized] = useState(false);
  const Icon = result.valid ? CircleCheckIcon : CircleXIcon;

  return (
    <section
      aria-live="polite"
      className={cn(
        'rounded-lg border px-4 py-3',
        result.valid ? 'border-emerald-500/30 bg-emerald-500/5' : 'border-destructive/30 bg-destructive/5',
      )}
    >
      <div className="flex items-start gap-2">
        <Icon
          className={cn('mt-0.5 size-4 shrink-0', result.valid ? 'text-emerald-600' : 'text-destructive')}
          aria-hidden
        />
        <div className="min-w-0 flex-1 space-y-2">
          <p className="text-sm font-medium">
            {result.valid ? t('validation.valid') : t('validation.invalid', { count: result.errors.length })}
            {outdated && (
              <span className="ml-2 text-xs font-normal text-muted-foreground">
                {t('validation.outdated')}
              </span>
            )}
          </p>
          {!result.valid && (
            <ul className="grid gap-1">
              {result.errors.map((error, i) => (
                <li key={i} className="font-mono text-xs break-words text-destructive">
                  {error}
                </li>
              ))}
            </ul>
          )}
          {result.valid && result.normalizedYaml && (
            <div className="space-y-2">
              <Button
                variant="link"
                size="sm"
                className="h-auto p-0"
                onClick={() => setShowNormalized((v) => !v)}
              >
                {showNormalized ? t('validation.hideNormalized') : t('validation.showNormalized')}
              </Button>
              {showNormalized && (
                <CodeEditor
                  value={result.normalizedYaml}
                  readOnly
                  height={320}
                  path={`policy-${policyId}-normalized.yaml`}
                  aria-label={t('validation.normalizedLabel')}
                />
              )}
            </div>
          )}
        </div>
        <Button variant="ghost" size="icon-sm" onClick={onClose} aria-label={t('validation.dismiss')}>
          <XIcon />
        </Button>
      </div>
    </section>
  );
}
