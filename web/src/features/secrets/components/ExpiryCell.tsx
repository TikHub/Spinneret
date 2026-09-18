import { CalendarClockIcon, CircleAlertIcon } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { TimeAgo } from '@/components/TimeAgo';
import { type Timestamp } from '@bufbuild/protobuf/wkt';
import { useNow } from '@/lib/clock';
import { cn } from '@/lib/utils';

import { expiryState } from '../secretTime';

/** Expiry time with warning colors: amber within 7 days, red when expired. */
export function ExpiryCell({ value, className }: { value: Timestamp | undefined; className?: string }) {
  const { t } = useTranslation('secrets');
  const now = useNow();
  const state = expiryState(value, now);
  if (state === 'none')
    return <span className={cn('text-muted-foreground', className)}>{t('expiry.never')}</span>;
  const tone =
    state === 'expired'
      ? 'text-destructive'
      : state === 'soon'
        ? 'text-amber-600 dark:text-amber-400'
        : 'text-foreground';
  const Icon = state === 'ok' ? CalendarClockIcon : CircleAlertIcon;
  return (
    <span className={cn('inline-flex items-center gap-1', tone, className)}>
      <Icon className="size-3.5 shrink-0" aria-hidden />
      {state === 'expired' && <span className="sr-only">{t('expiry.expired')}</span>}
      {state === 'soon' && <span className="sr-only">{t('expiry.soon')}</span>}
      <TimeAgo value={value} className={tone} />
    </span>
  );
}
