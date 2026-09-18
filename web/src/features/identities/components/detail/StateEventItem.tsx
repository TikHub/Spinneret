import { ArrowRightIcon, GhostIcon } from 'lucide-react';
import { type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';

import { IdText } from '@/components/CopyButton';
import { StateBadge } from '@/components/StateBadge';
import { TimeAgo } from '@/components/TimeAgo';
import { Badge } from '@/components/ui/badge';
import { type StateEvent } from '@/gen/spinneret/v1/common_pb';
import { formatDateTime, toDate } from '@/lib/time';

/** Label + value pair of the timeline meta row; wraps rather than overflowing. */
function Meta({ label, children }: { label: string; children: ReactNode }) {
  return (
    <span className="inline-flex max-w-full min-w-0 flex-wrap items-center gap-1">
      <span className="shrink-0 text-muted-foreground">{label}</span>
      {children}
    </span>
  );
}

/** One lifecycle or disposition event of the identity timeline. */
export function StateEventItem({ event }: { event: StateEvent }) {
  const { t } = useTranslation('identities');
  const until = toDate(event.until);
  const actionLabel = t(`events.actions.${event.action}`, { defaultValue: event.action });

  return (
    <li className="relative grid gap-1 border-l pb-4 pl-4 last:pb-1">
      <span
        className="absolute top-1.5 -left-[5px] size-2.5 rounded-full border-2 border-background bg-primary"
        aria-hidden
      />
      <div className="flex flex-wrap items-center gap-2 text-sm">
        <Badge variant="secondary">{actionLabel}</Badge>
        {(event.fromState || event.toState) && (
          <span className="inline-flex items-center gap-1">
            {event.fromState ? <StateBadge kind="identity" state={event.fromState} /> : '—'}
            <ArrowRightIcon className="size-3 text-muted-foreground" aria-label={t('events.to')} />
            {event.toState ? <StateBadge kind="identity" state={event.toState} /> : '—'}
          </span>
        )}
        {event.shadow && (
          <Badge variant="outline" className="border-violet-500/30 text-violet-700 dark:text-violet-400">
            <GhostIcon />
            {t('events.shadow')}
          </Badge>
        )}
        <span className="ml-auto text-xs text-muted-foreground">
          <TimeAgo value={event.createdAt} past />
        </span>
      </div>
      <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs">
        {event.scope && (
          <Meta label={t('events.scope')}>
            <span className="font-mono wrap-anywhere">{event.scope}</span>
          </Meta>
        )}
        {event.endpointGroup && (
          <Meta label={t('events.endpointGroup')}>
            <span className="font-mono wrap-anywhere">{event.endpointGroup}</span>
          </Meta>
        )}
        {(until || event.permanent) && (
          <Meta label={t('events.until')}>
            {event.permanent ? (
              <span className="font-medium text-rose-600 dark:text-rose-400">{t('events.permanent')}</span>
            ) : (
              <span className="tabular">{formatDateTime(until)}</span>
            )}
          </Meta>
        )}
        {event.rule && (
          <Meta label={t('events.rule')}>
            <span className="font-mono wrap-anywhere">{event.rule}</span>
            {event.policyId && (
              <span className="text-muted-foreground">
                ({event.policyId}
                {event.policyVersion > 0 ? ` v${event.policyVersion}` : ''})
              </span>
            )}
          </Meta>
        )}
        {event.outcome && (
          <Meta label={t('events.outcome')}>
            <span className="font-mono wrap-anywhere">{event.outcome}</span>
          </Meta>
        )}
        {event.reportId && (
          <Meta label={t('events.report')}>
            <IdText value={event.reportId} truncate={18} />
          </Meta>
        )}
        {event.actor && (
          <Meta label={t('events.actor')}>
            <span className="font-mono wrap-anywhere">{event.actor}</span>
          </Meta>
        )}
      </div>
      {event.reason && <p className="text-xs break-words text-muted-foreground">{event.reason}</p>}
    </li>
  );
}
