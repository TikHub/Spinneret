import { EyeOffIcon, ListPlusIcon } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { type DebugReportResponse } from '@/gen/spinneret/v1/policy_admin_pb';
import { cn } from '@/lib/utils';

import { counterKey, type CountEntry } from '../../debug';

export interface DebugResultProps {
  result: DebugReportResponse;
  counts: readonly CountEntry[];
  onAddCounters: () => void;
}

const RISK_OUTCOMES = new Set(['rate_limited', 'captcha', 'forbidden', 'banned']);

function outcomeTone(outcome: string): string {
  if (outcome === 'success')
    return 'border-emerald-500/30 bg-emerald-500/10 text-emerald-700 dark:text-emerald-400';
  if (RISK_OUTCOMES.has(outcome)) return 'border-rose-500/30 bg-rose-500/10 text-rose-700 dark:text-rose-400';
  if (outcome === 'unknown') return 'border-border bg-muted text-muted-foreground';
  return 'border-amber-500/30 bg-amber-500/10 text-amber-700 dark:text-amber-400';
}

/** Classification, planned actions and counters of a DebugReport run. */
export function DebugResult({ result, counts, onAddCounters }: DebugResultProps) {
  const { t } = useTranslation('policies');
  const present = new Set(counts.map((c) => counterKey(c.subject, c.outcome, c.window)));
  const missing = result.counters.filter((c) => !present.has(counterKey(c.subject, c.outcome, c.window)));

  return (
    <section aria-live="polite" className="grid gap-4 rounded-lg border bg-card p-4">
      <div className="grid gap-3 sm:grid-cols-4">
        <div className="grid gap-1">
          <span className="text-xs text-muted-foreground">{t('debugger.result.outcome')}</span>
          <span
            className={cn(
              'w-fit rounded-md border px-2 py-1 font-mono text-sm font-semibold',
              outcomeTone(result.outcome),
            )}
          >
            {result.outcome}
          </span>
        </div>
        <div className="grid gap-1">
          <span className="text-xs text-muted-foreground">{t('debugger.result.blame')}</span>
          <span className="font-mono text-sm">{result.blame || 'none'}</span>
        </div>
        <div className="grid gap-1">
          <span className="text-xs text-muted-foreground">{t('debugger.result.matchedRule')}</span>
          {result.matchedRuleIndex >= 0 ? (
            <span className="text-sm">
              <span className="font-mono text-muted-foreground">#{result.matchedRuleIndex + 1}</span>{' '}
              <span className="font-mono">{result.matchedRuleName || t('rules.unnamed')}</span>
            </span>
          ) : (
            <span className="text-sm text-muted-foreground">{t('debugger.result.noRule')}</span>
          )}
        </div>
        <div className="grid gap-1">
          <span className="text-xs text-muted-foreground">{t('debugger.result.mode')}</span>
          <Badge variant={result.mode === 'shadow' ? 'outline' : 'secondary'} className="gap-1">
            {result.mode === 'shadow' && <EyeOffIcon />}
            {t(`modes.${result.mode}`, { defaultValue: result.mode })}
          </Badge>
        </div>
      </div>
      {result.mode === 'shadow' && <p className="text-xs text-muted-foreground">{t('modeHints.shadow')}</p>}

      <div className="grid gap-2">
        <h3 className="text-sm font-semibold">
          {t('debugger.result.actions', { count: result.actions.length })}
        </h3>
        {result.actions.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t('debugger.result.noActions')}</p>
        ) : (
          <div className="overflow-x-auto rounded-md border">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t('debugger.result.columns.action')}</TableHead>
                  <TableHead>{t('debugger.result.columns.scope')}</TableHead>
                  <TableHead>{t('debugger.result.columns.duration')}</TableHead>
                  <TableHead className="text-right">{t('debugger.result.columns.severity')}</TableHead>
                  <TableHead>{t('debugger.result.columns.source')}</TableHead>
                  <TableHead>{t('debugger.result.columns.rule')}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {result.actions.map((a, i) => (
                  <TableRow key={`${a.scope}-${i}`}>
                    <TableCell className="font-mono text-xs">{a.action}</TableCell>
                    <TableCell className="font-mono text-xs">{a.scope}</TableCell>
                    <TableCell className="font-mono text-xs">
                      {a.permanent ? t('common:duration.permanent') : a.duration || '—'}
                    </TableCell>
                    <TableCell className="text-right tabular">{a.severity}</TableCell>
                    <TableCell>{t(`sources.${a.source}`, { defaultValue: a.source })}</TableCell>
                    <TableCell className="font-mono text-xs">
                      {a.ruleIndex >= 0 ? `#${a.ruleIndex + 1} ` : ''}
                      {a.ruleName || '—'}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        )}
      </div>

      <div className="grid gap-2">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h3 className="text-sm font-semibold">{t('debugger.result.counters')}</h3>
          {missing.length > 0 && (
            <Button variant="outline" size="sm" onClick={onAddCounters}>
              <ListPlusIcon />
              {t('debugger.result.addCounters', { count: missing.length })}
            </Button>
          )}
        </div>
        {result.counters.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t('debugger.result.noCounters')}</p>
        ) : (
          <ul className="flex flex-wrap gap-1.5">
            {result.counters.map((c) => {
              const key = counterKey(c.subject, c.outcome, c.window);
              return (
                <li key={key}>
                  <code
                    className={cn(
                      'rounded border px-1.5 py-0.5 font-mono text-xs',
                      present.has(key) ? 'bg-muted' : 'border-dashed text-muted-foreground',
                    )}
                    title={
                      present.has(key) ? t('debugger.result.counterSet') : t('debugger.result.counterDefault')
                    }
                  >
                    {key}
                  </code>
                </li>
              );
            })}
          </ul>
        )}
      </div>
    </section>
  );
}
