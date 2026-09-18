import { type ResolvedTheme } from '@/app/theme/ThemeProvider';

/** Classified report outcomes (proto RequestEvent.outcome). */
export const OUTCOMES = [
  'success',
  'empty',
  'rate_limited',
  'captcha',
  'auth_invalid',
  'forbidden',
  'banned',
  'proxy_error',
  'network_error',
  'target_error',
  'client_error',
  'unknown',
] as const;
export type Outcome = (typeof OUTCOMES)[number];

/** Non-success outcomes stored as risk events. */
export const RISK_EVENT_OUTCOMES = OUTCOMES.filter((o) => o !== 'success');

/** Outcomes counted in the risk ratio (same set as the overview). */
export const RISK_RATIO_OUTCOMES: readonly Outcome[] = ['rate_limited', 'captcha', 'forbidden', 'banned'];

export function isOutcome(value: string): value is Outcome {
  return (OUTCOMES as readonly string[]).includes(value);
}

/** Badge classes per outcome (both themes). */
const OUTCOME_BADGE_CLASS: Record<Outcome, string> = {
  success: 'border-emerald-500/25 bg-emerald-500/10 text-emerald-700 dark:text-emerald-400',
  empty: 'border-amber-500/25 bg-amber-500/10 text-amber-700 dark:text-amber-400',
  rate_limited: 'border-orange-500/25 bg-orange-500/10 text-orange-700 dark:text-orange-400',
  captcha: 'border-rose-500/25 bg-rose-500/10 text-rose-700 dark:text-rose-400',
  auth_invalid: 'border-violet-500/25 bg-violet-500/10 text-violet-700 dark:text-violet-400',
  forbidden: 'border-rose-600/25 bg-rose-600/10 text-rose-800 dark:text-rose-300',
  banned: 'border-red-600/25 bg-red-600/10 text-red-700 dark:text-red-400',
  proxy_error: 'border-sky-500/25 bg-sky-500/10 text-sky-700 dark:text-sky-400',
  network_error: 'border-cyan-500/25 bg-cyan-500/10 text-cyan-700 dark:text-cyan-400',
  target_error: 'border-indigo-500/25 bg-indigo-500/10 text-indigo-700 dark:text-indigo-400',
  client_error: 'border-fuchsia-500/25 bg-fuchsia-500/10 text-fuchsia-700 dark:text-fuchsia-400',
  unknown: 'border-zinc-500/25 bg-zinc-500/10 text-zinc-600 dark:text-zinc-400',
};

export function outcomeBadgeClass(outcome: string): string {
  return isOutcome(outcome) ? OUTCOME_BADGE_CLASS[outcome] : OUTCOME_BADGE_CLASS.unknown;
}

/** Chart colors per outcome (hex for the canvas renderer); a color follows its outcome. */
const OUTCOME_CHART_COLORS: Record<Outcome, Record<ResolvedTheme, string>> = {
  success: { light: '#059669', dark: '#34d399' },
  empty: { light: '#d97706', dark: '#fbbf24' },
  rate_limited: { light: '#ea580c', dark: '#fb923c' },
  captcha: { light: '#e11d48', dark: '#fb7185' },
  auth_invalid: { light: '#7c3aed', dark: '#a78bfa' },
  forbidden: { light: '#9f1239', dark: '#fda4af' },
  banned: { light: '#b91c1c', dark: '#f87171' },
  proxy_error: { light: '#0284c7', dark: '#38bdf8' },
  network_error: { light: '#0891b2', dark: '#22d3ee' },
  target_error: { light: '#4f46e5', dark: '#818cf8' },
  client_error: { light: '#c026d3', dark: '#e879f9' },
  unknown: { light: '#64748b', dark: '#94a3b8' },
};

export const OTHER_OUTCOMES_COLOR: Record<ResolvedTheme, string> = { light: '#cbd5e1', dark: '#475569' };

export function outcomeChartColor(outcome: string, theme: ResolvedTheme): string {
  return isOutcome(outcome) ? OUTCOME_CHART_COLORS[outcome][theme] : OTHER_OUTCOMES_COLOR[theme];
}

/** One slice of the outcome distribution. */
export interface OutcomeSlice {
  /** Outcome name, or undefined for the folded "other" slice. */
  outcome: string | undefined;
  count: number;
  ratio: number;
}

/** Maximum distinct slices before the smallest ones fold into "other". */
export const MAX_OUTCOME_SLICES = 7;

/**
 * Sorts outcome counts descending and folds the tail beyond `maxSlices` into
 * one "other" slice so the donut stays readable.
 */
export function outcomeSlices(
  counts: Readonly<Record<string, bigint | number>>,
  maxSlices = MAX_OUTCOME_SLICES,
): OutcomeSlice[] {
  const entries = Object.entries(counts)
    .map(([outcome, value]) => ({ outcome, count: Number(value) }))
    .filter((e) => Number.isFinite(e.count) && e.count > 0)
    .sort((a, b) => b.count - a.count || a.outcome.localeCompare(b.outcome));
  const total = entries.reduce((sum, e) => sum + e.count, 0);
  if (total === 0) return [];
  const ratio = (count: number) => count / total;
  if (entries.length <= maxSlices) {
    return entries.map((e) => ({ ...e, ratio: ratio(e.count) }));
  }
  const head = entries.slice(0, maxSlices - 1).map((e) => ({ ...e, ratio: ratio(e.count) }));
  const rest = entries.slice(maxSlices - 1).reduce((sum, e) => sum + e.count, 0);
  return [...head, { outcome: undefined, count: rest, ratio: ratio(rest) }];
}

/** Share of events with the given outcomes (0 when there are none). */
export function outcomeRatio(
  counts: Readonly<Record<string, bigint | number>>,
  outcomes: readonly string[],
): number {
  let total = 0;
  let matched = 0;
  for (const [outcome, value] of Object.entries(counts)) {
    const n = Number(value);
    total += n;
    if (outcomes.includes(outcome)) matched += n;
  }
  return total > 0 ? matched / total : 0;
}
