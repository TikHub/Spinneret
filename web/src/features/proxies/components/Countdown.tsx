import { useCallback, useSyncExternalStore } from 'react';
import { useTranslation } from 'react-i18next';

import { SimpleTooltip } from '@/components/ui/tooltip';
import { humanizeDuration } from '@/lib/duration';
import { formatDateTime, remainingMs, toDate, type TimeInput } from '@/lib/time';

const TICK_MS = 1000;

/** Current time truncated to the tick, so consecutive reads within one tick are equal. */
function currentTick(): number {
  return Math.floor(Date.now() / TICK_MS) * TICK_MS;
}

export interface CountdownProps {
  until: TimeInput;
  /** Rendered when the time is unset or has passed. */
  fallback?: string;
  className?: string;
}

/** Live "time left" until a timestamp, ticking every second while running. */
export function Countdown({ until, fallback = '—', className }: CountdownProps) {
  const { t } = useTranslation();
  const date = toDate(until);
  const untilMs = date?.getTime();
  // Every render reads the current time, so a changed end time is never compared with a clock
  // that stopped when an earlier countdown ended; the interval only drives re-renders.
  const subscribe = useCallback(
    (onTick: () => void) => {
      if (untilMs === undefined || currentTick() >= untilMs) return () => undefined;
      const timer = setInterval(() => {
        onTick();
        if (currentTick() >= untilMs) clearInterval(timer);
      }, TICK_MS);
      return () => clearInterval(timer);
    },
    [untilMs],
  );
  const now = useSyncExternalStore(subscribe, currentTick, currentTick);
  const left = remainingMs(date, now);

  if (!date || left <= 0) return <span className="text-muted-foreground">{fallback}</span>;
  return (
    <SimpleTooltip content={formatDateTime(date)}>
      <time dateTime={date.toISOString()} className={className} tabIndex={0}>
        {humanizeDuration(left, { t })}
      </time>
    </SimpleTooltip>
  );
}
