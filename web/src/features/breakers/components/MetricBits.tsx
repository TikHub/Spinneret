import { InfinityIcon, TimerIcon } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { SimpleTooltip } from '@/components/ui/tooltip';
import { type Timestamp } from '@bufbuild/protobuf/wkt';
import { formatPercent } from '@/lib/format';
import { formatDateTime } from '@/lib/time';
import { cn } from '@/lib/utils';

import { clampRatio, formatCountdown, openPeriod, useSecondTicker } from '../countdown';

export interface RatioBarProps {
  label: string;
  ratio: number;
  tone: 'success' | 'risk';
  /** Trip threshold marker (0..1); omitted when unknown. */
  className?: string;
}

/** Small labeled ratio bar (success or risk share of the window). */
export function RatioBar({ label, ratio, tone, className }: RatioBarProps) {
  const { i18n } = useTranslation();
  const value = clampRatio(ratio);
  const text = formatPercent(value, 0, i18n.language);
  return (
    <div className={cn('grid grid-cols-[2.75rem_4rem_2.5rem] items-center gap-1.5 text-xs', className)}>
      <span className="text-muted-foreground">{label}</span>
      <div
        role="meter"
        aria-label={label}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={Math.round(value * 100)}
        aria-valuetext={text}
        className="h-1.5 overflow-hidden rounded-full bg-muted"
      >
        <div
          className={cn('h-full rounded-full', tone === 'success' ? 'bg-emerald-500' : 'bg-rose-500')}
          style={{ width: `${value * 100}%` }}
        />
      </div>
      <span className="text-right tabular">{text}</span>
    </div>
  );
}

/** Live countdown to the end of an open period ("Indefinite" when opened without an end). */
export function OpenUntil({ state, openUntil }: { state: string; openUntil: Timestamp | undefined }) {
  const { t } = useTranslation('breakers');
  if (state !== 'open') return <span className="text-muted-foreground">—</span>;
  if (!openUntil || (openUntil.seconds === 0n && openUntil.nanos === 0)) {
    return (
      <span className="inline-flex items-center gap-1 text-rose-700 dark:text-rose-400">
        <InfinityIcon className="size-3.5" aria-hidden />
        {t('indefinite')}
      </span>
    );
  }
  return <Countdown state={state} openUntil={openUntil} />;
}

function Countdown({ state, openUntil }: { state: string; openUntil: Timestamp }) {
  const { t } = useTranslation('breakers');
  const now = useSecondTicker();
  const period = openPeriod(state, openUntil, now);
  if (period.kind !== 'until') return null;
  return (
    <SimpleTooltip content={formatDateTime(period.until)}>
      <span className="inline-flex items-center gap-1 font-mono text-xs tabular">
        <TimerIcon className="size-3.5 text-muted-foreground" aria-hidden />
        {period.remainingMs > 0 ? formatCountdown(period.remainingMs) : t('halfOpenSoon')}
      </span>
    </SimpleTooltip>
  );
}
