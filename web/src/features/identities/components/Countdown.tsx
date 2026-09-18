import { useTranslation } from 'react-i18next';

import { SimpleTooltip } from '@/components/ui/tooltip';
import { CLOCK_TICK_MS, useNow } from '@/lib/clock';
import { formatDateTime, toDate, type TimeInput } from '@/lib/time';
import { cn } from '@/lib/utils';

import { formatCountdown } from '../hotState';
import { useSecondTicker } from '../useTicker';

export interface CountdownProps {
  until: TimeInput;
  /** Rendered when unset. */
  fallback?: string;
  /** Rendered when the time has passed (default "Ended"). */
  ended?: string;
  className?: string;
}

/** Live "in 4m 12s" countdown with the absolute time in a tooltip; past times render as ended. */
export function Countdown({ until, fallback = '—', ended, className }: CountdownProps) {
  const { t } = useTranslation('identities');
  const date = toDate(until);
  // The coarse shared clock decides whether a per-second tick is needed at all.
  const coarseNow = useNow();
  const now = useSecondTicker(date !== undefined && date.getTime() > coarseNow - CLOCK_TICK_MS);
  if (!date) return <span className={cn('text-muted-foreground', className)}>{fallback}</span>;
  const text = formatCountdown(date, now, (part) => t(`countdown.units.${part.unit}`, { value: part.value }));
  return (
    <SimpleTooltip content={formatDateTime(date)}>
      <time
        dateTime={date.toISOString()}
        className={cn('tabular whitespace-nowrap', !text && 'text-muted-foreground', className)}
      >
        {text ? t('countdown.remaining', { time: text }) : (ended ?? t('countdown.ended'))}
      </time>
    </SimpleTooltip>
  );
}
