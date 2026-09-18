import { useTranslation } from 'react-i18next';

import { STATE_TONE_DOT_CLASS, stateTone, type StateKind, type StateTone } from '@/lib/states';
import { cn } from '@/lib/utils';

/** Tone classes per state color (spec section 5). */
const TONES: Record<StateTone, string> = {
  emerald: 'border-emerald-500/25 bg-emerald-500/10 text-emerald-700 dark:text-emerald-400',
  sky: 'border-sky-500/25 bg-sky-500/10 text-sky-700 dark:text-sky-400',
  amber: 'border-amber-500/25 bg-amber-500/10 text-amber-700 dark:text-amber-400',
  rose: 'border-rose-500/25 bg-rose-500/10 text-rose-700 dark:text-rose-400',
  orange: 'border-orange-500/25 bg-orange-500/10 text-orange-700 dark:text-orange-400',
  zinc: 'border-zinc-500/25 bg-zinc-500/10 text-zinc-600 dark:text-zinc-400',
  stone: 'border-stone-500/25 bg-stone-500/10 text-stone-600 dark:text-stone-400',
  red: 'border-red-600/25 bg-red-600/10 text-red-700 dark:text-red-400',
  indigo: 'border-indigo-500/25 bg-indigo-500/10 text-indigo-700 dark:text-indigo-400',
};

export interface StateBadgeProps {
  state: string;
  kind?: StateKind;
  /** Overrides the translated label (states.<state> in common). */
  label?: string;
  className?: string;
}

/** Colored state pill for identities, proxies, accounts, breakers and sites. */
export function StateBadge({ state, kind = 'identity', label, className }: StateBadgeProps) {
  const { t } = useTranslation();
  const tone = stateTone(kind, state);
  const text = label ?? t(`states.${state || 'unknown'}`, { defaultValue: state || t('states.unknown') });
  return (
    <span
      className={cn(
        'inline-flex w-fit items-center gap-1.5 rounded-full border px-2 py-0.5 text-xs font-medium whitespace-nowrap',
        TONES[tone],
        className,
      )}
      data-state={state}
    >
      <span className={cn('size-1.5 rounded-full', STATE_TONE_DOT_CLASS[tone])} aria-hidden />
      {text}
    </span>
  );
}
