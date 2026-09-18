import { useMutation } from '@tanstack/react-query';
import { BugPlayIcon, LoaderCircleIcon, RotateCcwIcon } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';

import { useAuth } from '@/app/auth/AuthContext';
import { ErrorState } from '@/components/ErrorState';
import { Button } from '@/components/ui/button';
import { Checkbox } from '@/components/ui/checkbox';
import { FormField } from '@/components/ui/form';
import { Input } from '@/components/ui/input';
import { type Policy } from '@/gen/spinneret/v1/policy_admin_pb';
import { policyClient } from '@/lib/clients';

import {
  buildDebugRequest,
  countEntriesFor,
  emptyDebugForm,
  type BuildDebugResult,
  type DebugForm,
  type DebugRequestInit,
  type DebugTargetMode,
} from '../../debug';
import { ScopeSelects } from '../bindings/ScopeSelects';
import { ContextForm } from './ContextForm';
import { CountsEditor } from './CountsEditor';
import { DebugResult } from './DebugResult';
import { ReportFieldsForm } from './ReportFieldsForm';

export interface DebuggerPanelProps {
  /** Signal or action policy whose draft can replace the resolved policy. */
  draftPolicy?: Policy;
  /** The editor has unsaved changes (the debugger uses the saved draft). */
  hasUnsavedChanges?: boolean;
}

type Failure = Extract<BuildDebugResult, { ok: false }>;

const NO_ERRORS: Failure = { ok: false, fieldErrors: {}, countErrors: {}, banCountErrors: {} };
const TARGET_MODES: readonly DebugTargetMode[] = ['endpoint_group', 'uri', 'report_uri'];

/** Rule debugger: simulates a report against the resolved (or draft) policies without side effects. */
export function DebuggerPanel({ draftPolicy, hasUnsavedChanges }: DebuggerPanelProps) {
  const { t } = useTranslation('policies');
  const { namespaceName } = useAuth();
  const [form, setForm] = useState<DebugForm>(() => ({
    ...emptyDebugForm(),
    useDraft: draftPolicy !== undefined,
  }));
  const [errors, setErrors] = useState<Failure>(NO_ERRORS);
  const debug = useMutation({
    mutationFn: (request: DebugRequestInit) => policyClient.debugReport(request),
  });

  const run = () => {
    const built = buildDebugRequest(form, namespaceName ?? '', draftPolicy?.id);
    if (!built.ok) {
      setErrors(built);
      return;
    }
    setErrors(NO_ERRORS);
    debug.mutate(built.request);
  };
  const set = <K extends keyof DebugForm>(key: K, value: DebugForm[K]) =>
    setForm((f) => ({ ...f, [key]: value }));
  const fieldError = (field: string) => {
    const e = errors.fieldErrors[field];
    return e ? t(`debugger.errors.${e}`) : undefined;
  };

  return (
    <div className="grid gap-4">
      <section className="grid gap-3 rounded-lg border bg-card p-4">
        <div className="space-y-0.5">
          <h3 className="text-sm font-semibold">{t('debugger.targetTitle')}</h3>
          <p className="text-xs text-muted-foreground">{t('debugger.targetHint')}</p>
        </div>
        <ScopeSelects
          value={{ site: form.site, client: form.client, endpointGroup: form.endpointGroup }}
          onChange={(scope) => setForm((f) => ({ ...f, ...scope }))}
          required={form.targetMode === 'endpoint_group' ? 'endpoint_group' : 'client'}
          hideEndpointGroup={form.targetMode !== 'endpoint_group'}
        />
        {(fieldError('site') || fieldError('client') || fieldError('endpointGroup')) && (
          <p role="alert" className="text-xs text-destructive">
            {t('debugger.targetRequired')}
          </p>
        )}
        <fieldset className="grid gap-2">
          <legend className="mb-1 text-sm font-medium">{t('debugger.targetMode')}</legend>
          <div role="radiogroup" className="flex flex-wrap gap-1.5">
            {TARGET_MODES.map((mode) => (
              <Button
                key={mode}
                role="radio"
                aria-checked={form.targetMode === mode}
                variant={form.targetMode === mode ? 'secondary' : 'outline'}
                size="sm"
                onClick={() => set('targetMode', mode)}
              >
                {t(`debugger.targetModes.${mode}`)}
              </Button>
            ))}
          </div>
        </fieldset>
        {form.targetMode === 'uri' && (
          <FormField
            label={t('fields.matchUri')}
            required
            error={fieldError('uri')}
            description={t('debugger.matchUriHint')}
          >
            <Input
              value={form.uri}
              onChange={(e) => set('uri', e.target.value)}
              placeholder="/api/item/123"
              maxLength={2048}
              className="font-mono"
            />
          </FormField>
        )}
        {draftPolicy && (
          <div className="grid gap-1">
            <label className="inline-flex items-center gap-2 text-sm">
              <Checkbox
                checked={form.useDraft}
                onCheckedChange={(checked) => set('useDraft', checked === true)}
              />
              {t('debugger.useDraft', { name: draftPolicy.name })}
            </label>
            {form.useDraft && hasUnsavedChanges && (
              <p className="text-xs text-amber-700 dark:text-amber-400">{t('debugger.unsavedHint')}</p>
            )}
            {form.useDraft && !draftPolicy.hasDraft && (
              <p className="text-xs text-muted-foreground">{t('debugger.noDraftHint')}</p>
            )}
          </div>
        )}
      </section>

      <ReportFieldsForm
        value={form.report}
        onChange={(report) => set('report', report)}
        errors={errors.fieldErrors}
        uriRequired={form.targetMode === 'report_uri'}
      />
      <ContextForm
        value={form.context}
        onChange={(context) => set('context', context)}
        errors={errors.fieldErrors}
      />
      <CountsEditor
        counts={form.counts}
        banCounts={form.banCounts}
        onCountsChange={(counts) => set('counts', counts)}
        onBanCountsChange={(banCounts) => set('banCounts', banCounts)}
        countErrors={errors.countErrors}
        banCountErrors={errors.banCountErrors}
      />

      <div className="flex flex-wrap items-center gap-2">
        <Button onClick={run} disabled={debug.isPending || !namespaceName}>
          {debug.isPending ? <LoaderCircleIcon className="animate-spin" /> : <BugPlayIcon />}
          {t('debugger.run')}
        </Button>
        <Button
          variant="ghost"
          onClick={() => {
            setForm({ ...emptyDebugForm(), useDraft: draftPolicy !== undefined });
            setErrors(NO_ERRORS);
            debug.reset();
          }}
        >
          <RotateCcwIcon />
          {t('debugger.reset')}
        </Button>
        {(Object.keys(errors.fieldErrors).length > 0 ||
          Object.keys(errors.countErrors).length > 0 ||
          Object.keys(errors.banCountErrors).length > 0) && (
          <span role="alert" className="text-sm text-destructive">
            {t('debugger.fixErrors')}
          </span>
        )}
      </div>

      {debug.isError && (
        <ErrorState error={debug.error} onRetry={run} compact className="rounded-lg border" />
      )}
      {debug.data && !debug.isError && (
        <DebugResult
          result={debug.data}
          counts={form.counts}
          onAddCounters={() => {
            const counters = debug.data.counters;
            setForm((f) => ({ ...f, counts: [...f.counts, ...countEntriesFor(counters, f.counts)] }));
          }}
        />
      )}
    </div>
  );
}
