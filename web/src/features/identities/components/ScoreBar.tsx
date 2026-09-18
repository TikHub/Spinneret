import { useTranslation } from 'react-i18next';

import { SimpleTooltip } from '@/components/ui/tooltip';
import { formatNumber } from '@/lib/format';
import { cn } from '@/lib/utils';

import { clampScore, formatScore, scoreTone, type ScoreTone } from '../hotState';

const BAR_CLASS: Record<ScoreTone, string> = {
  good: 'bg-emerald-500',
  warn: 'bg-amber-500',
  bad: 'bg-rose-500',
};

export interface ScoreBarProps {
  score: number;
  /** Observations behind the score; shown in the tooltip. */
  samples?: number;
  className?: string;
}

/** Health score (0..100) as a small colored bar with the value. */
export function ScoreBar({ score, samples, className }: ScoreBarProps) {
  const { t, i18n } = useTranslation('identities');
  const value = clampScore(score);
  const label = t('score.label', { score: formatScore(value) });
  const tooltip =
    samples === undefined
      ? label
      : `${label} · ${t('score.samples', { count: samples, formatted: formatNumber(samples, undefined, i18n.language) })}`;
  return (
    <SimpleTooltip content={tooltip}>
      <span className={cn('inline-flex items-center gap-2', className)}>
        <span
          className="relative h-1.5 w-16 overflow-hidden rounded-full bg-muted"
          role="meter"
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={value}
          aria-label={label}
        >
          <span
            className={cn('absolute inset-y-0 left-0 rounded-full', BAR_CLASS[scoreTone(value)])}
            style={{ width: `${value}%` }}
          />
        </span>
        <span className="tabular w-8 text-right text-xs">{formatScore(value)}</span>
      </span>
    </SimpleTooltip>
  );
}
