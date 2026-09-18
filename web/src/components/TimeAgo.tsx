import { useTranslation } from 'react-i18next';

import { SimpleTooltip } from '@/components/ui/tooltip';
import { useNow } from '@/lib/clock';
import { formatDateTime, formatRelative, toDate, type TimeInput } from '@/lib/time';
import { cn } from '@/lib/utils';

export interface TimeAgoProps {
  value: TimeInput;
  /** Rendered when the value is unset; defaults to "—". */
  fallback?: string;
  /** Treat future values as "now" (for server timestamps affected by clock skew, e.g. "updated at"). */
  past?: boolean;
  className?: string;
}

/** Relative time ("3 minutes ago") with the absolute local time in a tooltip. */
export function TimeAgo({ value, fallback = '—', past = false, className }: TimeAgoProps) {
  const { i18n } = useTranslation();
  const now = useNow();
  const date = toDate(value);
  if (!date) return <span className={cn('text-muted-foreground', className)}>{fallback}</span>;
  const reference = past ? Math.max(now, date.getTime()) : now;
  const relative = formatRelative(date, reference, i18n.language);
  return (
    <SimpleTooltip content={formatDateTime(date)}>
      <time dateTime={date.toISOString()} className={cn('whitespace-nowrap', className)}>
        {relative}
      </time>
    </SimpleTooltip>
  );
}
