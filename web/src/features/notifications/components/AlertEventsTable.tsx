import {
  ChevronDownIcon,
  ChevronRightIcon,
  CircleCheckIcon,
  CircleXIcon,
  RefreshCwIcon,
  SirenIcon,
  TriangleAlertIcon,
} from 'lucide-react';
import { Fragment, useState } from 'react';
import { useTranslation } from 'react-i18next';

import { IdText } from '@/components/CopyButton';
import { EmptyState } from '@/components/EmptyState';
import { ErrorState } from '@/components/ErrorState';
import { JsonView } from '@/components/JsonView';
import { TimeAgo } from '@/components/TimeAgo';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Skeleton } from '@/components/ui/skeleton';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { type AlertEvent } from '@/gen/spinneret/v1/notification_admin_pb';
import { errorMessage } from '@/lib/errors';
import { cn } from '@/lib/utils';

import { SeverityBadge } from './badges';

const COLUMNS = 7;

function DeliverySummary({ event }: { event: AlertEvent }) {
  const { t } = useTranslation('notifications');
  const total = event.deliveries.length;
  if (total === 0) return <span className="text-muted-foreground">{t('alerts.noDeliveries')}</span>;
  const ok = event.deliveries.filter((d) => d.ok).length;
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1 tabular',
        ok === total ? 'text-emerald-600 dark:text-emerald-400' : 'text-destructive',
      )}
    >
      {ok === total ? (
        <CircleCheckIcon className="size-3.5" aria-hidden />
      ) : (
        <CircleXIcon className="size-3.5" aria-hidden />
      )}
      {t('alerts.delivered', { ok, total })}
    </span>
  );
}

function EventDetails({ event }: { event: AlertEvent }) {
  const { t } = useTranslation('notifications');
  const hasDetails = event.details !== undefined && Object.keys(event.details).length > 0;
  return (
    <div className="grid gap-3 bg-muted/30 px-4 py-3 lg:grid-cols-2">
      <div className="grid content-start gap-2">
        {event.message && <p className="text-sm break-words whitespace-pre-wrap">{event.message}</p>}
        <IdText value={event.id} className="text-muted-foreground" />
        {hasDetails && <JsonView value={event.details} collapseDepth={1} />}
      </div>
      <div className="grid content-start gap-1">
        <p className="text-xs font-medium text-muted-foreground">{t('alerts.deliveries')}</p>
        {event.deliveries.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t('alerts.noDeliveriesHint')}</p>
        ) : (
          <ul className="grid gap-1">
            {event.deliveries.map((d, i) => (
              <li
                key={`${d.channelId}-${i}`}
                className="flex flex-wrap items-center gap-2 rounded-md border bg-card px-2 py-1.5 text-sm"
              >
                {d.ok ? (
                  <CircleCheckIcon
                    className="size-4 text-emerald-600 dark:text-emerald-400"
                    aria-label={t('alerts.ok')}
                  />
                ) : (
                  <CircleXIcon className="size-4 text-destructive" aria-label={t('alerts.failed')} />
                )}
                <span className="font-medium">{d.channelName || d.channelId}</span>
                <IdText value={d.channelId} className="text-muted-foreground" />
                <TimeAgo value={d.at} past className="ml-auto text-xs text-muted-foreground" />
                {!d.ok && d.error && <p className="w-full text-xs break-words text-destructive">{d.error}</p>}
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}

export interface AlertEventsTableProps {
  events: readonly AlertEvent[] | undefined;
  isLoading: boolean;
  error: unknown;
  onRetry: () => void;
  filtered: boolean;
}

/** Fired alerts with expandable per-channel delivery results. */
export function AlertEventsTable({ events, isLoading, error, onRetry, filtered }: AlertEventsTableProps) {
  const { t } = useTranslation('notifications');
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(new Set());
  const toggle = (id: string) =>
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  const rows = events ?? [];
  const hasError = error !== undefined && error !== null;

  return (
    <div className="max-h-[70vh] overflow-auto">
      {hasError && rows.length > 0 && (
        // A failed background refresh keeps the previous rows; say that they may be stale.
        <div
          role="alert"
          className="flex flex-wrap items-center gap-2 border-b border-destructive/30 bg-destructive/5 px-3 py-1.5 text-sm text-destructive"
        >
          <TriangleAlertIcon className="size-4 shrink-0" aria-hidden />
          <span className="min-w-0 flex-1 break-words">
            {t('common:table.refreshFailed')} {errorMessage(error, t)}
          </span>
          <Button variant="outline" size="sm" className="h-7" onClick={onRetry}>
            <RefreshCwIcon />
            {t('common:actions.retry')}
          </Button>
        </div>
      )}
      <Table>
        <TableHeader className="sticky top-0 z-10 bg-muted/80 backdrop-blur">
          <TableRow>
            <TableHead className="w-8">
              <span className="sr-only">{t('alerts.expand')}</span>
            </TableHead>
            <TableHead>{t('alerts.time')}</TableHead>
            <TableHead>{t('alerts.severity')}</TableHead>
            <TableHead>{t('alerts.kind')}</TableHead>
            <TableHead>{t('alerts.title')}</TableHead>
            <TableHead>{t('alerts.scope')}</TableHead>
            <TableHead>{t('alerts.deliveries')}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {isLoading &&
            rows.length === 0 &&
            Array.from({ length: 6 }, (_, i) => (
              <TableRow key={`skeleton-${i}`}>
                {Array.from({ length: COLUMNS }, (_, j) => (
                  <TableCell key={j}>
                    <Skeleton className="h-4 w-full max-w-32" />
                  </TableCell>
                ))}
              </TableRow>
            ))}
          {!isLoading && hasError && rows.length === 0 && (
            <TableRow>
              <TableCell colSpan={COLUMNS}>
                <ErrorState error={error} onRetry={onRetry} compact />
              </TableCell>
            </TableRow>
          )}
          {!isLoading && !hasError && rows.length === 0 && (
            <TableRow>
              <TableCell colSpan={COLUMNS}>
                <EmptyState
                  compact
                  icon={SirenIcon}
                  title={filtered ? t('alerts.noMatches') : t('alerts.empty')}
                  description={filtered ? undefined : t('alerts.emptyDescription')}
                />
              </TableCell>
            </TableRow>
          )}
          {rows.map((event) => {
            const open = expanded.has(event.id);
            const Chevron = open ? ChevronDownIcon : ChevronRightIcon;
            return (
              <Fragment key={event.id}>
                <TableRow
                  className="cursor-pointer"
                  data-state={open ? 'selected' : undefined}
                  onClick={() => toggle(event.id)}
                >
                  <TableCell>
                    <button
                      type="button"
                      className="rounded p-0.5 text-muted-foreground hover:text-foreground"
                      aria-expanded={open}
                      aria-label={open ? t('alerts.collapse') : t('alerts.expand')}
                      onClick={(e) => {
                        e.stopPropagation();
                        toggle(event.id);
                      }}
                    >
                      <Chevron className="size-4" />
                    </button>
                  </TableCell>
                  <TableCell>
                    <TimeAgo value={event.createdAt} past />
                  </TableCell>
                  <TableCell>
                    <SeverityBadge severity={event.severity} />
                  </TableCell>
                  <TableCell>
                    <Badge variant="outline">
                      {t(`alertKinds.${event.kind}.label`, { defaultValue: event.kind })}
                    </Badge>
                  </TableCell>
                  <TableCell className="max-w-96 truncate" title={event.title}>
                    {event.title}
                  </TableCell>
                  <TableCell className="font-mono text-xs">
                    {event.namespace ? (
                      <>
                        {event.namespace}
                        {event.site && <span className="text-muted-foreground"> / {event.site}</span>}
                      </>
                    ) : (
                      <span className="font-sans text-muted-foreground">{t('scope.tenant')}</span>
                    )}
                  </TableCell>
                  <TableCell>
                    <DeliverySummary event={event} />
                  </TableCell>
                </TableRow>
                {open && (
                  <TableRow className="hover:bg-transparent">
                    <TableCell colSpan={COLUMNS} className="p-0 whitespace-normal">
                      <EventDetails event={event} />
                    </TableCell>
                  </TableRow>
                )}
              </Fragment>
            );
          })}
        </TableBody>
      </Table>
    </div>
  );
}
