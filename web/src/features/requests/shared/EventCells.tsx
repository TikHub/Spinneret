import { Link } from '@tanstack/react-router';
import { useTranslation } from 'react-i18next';

import { useAuth } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';
import { CopyButton, IdText } from '@/components/CopyButton';
import { SimpleTooltip } from '@/components/ui/tooltip';
import { type RequestEvent } from '@/gen/spinneret/v1/dashboard_pb';
import { useNow } from '@/lib/clock';
import { formatDateTime, formatRelative, toDate, type TimeInput } from '@/lib/time';
import { cn } from '@/lib/utils';

import { outcomeBadgeClass } from './outcomes';

/** Colored pill of a classified outcome. */
export function OutcomeBadge({ outcome, className }: { outcome: string; className?: string }) {
  const { t } = useTranslation('requests');
  const value = outcome || 'unknown';
  return (
    <span
      className={cn(
        'inline-flex w-fit items-center rounded-full border px-2 py-0.5 text-xs font-medium whitespace-nowrap',
        outcomeBadgeClass(value),
        className,
      )}
      data-outcome={value}
    >
      {t(`shared.outcomes.${value}`, { defaultValue: value })}
    </span>
  );
}

/** Muted dash for empty values. */
export function Dash() {
  return <span className="text-muted-foreground">—</span>;
}

/** Plain monospace value or a dash; long tokens wrap instead of escaping their cell. */
export function MonoText({ value, className }: { value: string | undefined; className?: string }) {
  if (!value) return <Dash />;
  return <span className={cn('font-mono text-xs wrap-anywhere', className)}>{value}</span>;
}

/** HTTP status colored by class; 0 means no response. */
export function HttpStatus({ status }: { status: number }) {
  const { t } = useTranslation('requests');
  if (status === 0) {
    return <span className="text-xs text-muted-foreground">{t('shared.noResponse')}</span>;
  }
  const tone =
    status >= 500
      ? 'text-rose-600 dark:text-rose-400'
      : status >= 400
        ? 'text-amber-600 dark:text-amber-400'
        : status >= 300
          ? 'text-sky-600 dark:text-sky-400'
          : 'text-emerald-600 dark:text-emerald-400';
  return <span className={cn('tabular font-mono text-xs font-medium', tone)}>{status}</span>;
}

/** Node markers as compact chips (at most `max`, the rest in a tooltip). */
export function MarkerList({ markers, max = 3 }: { markers: readonly string[]; max?: number }) {
  if (markers.length === 0) return <Dash />;
  const shown = markers.slice(0, max);
  const hidden = markers.slice(max);
  return (
    <span className="inline-flex max-w-full flex-wrap items-center gap-1">
      {shown.map((marker) => (
        <span
          key={marker}
          className="rounded bg-muted px-1.5 py-0.5 font-mono text-[11px] wrap-anywhere text-muted-foreground"
        >
          {marker}
        </span>
      ))}
      {hidden.length > 0 && (
        <SimpleTooltip content={hidden.join(', ')}>
          <span tabIndex={0} className="text-xs text-muted-foreground">
            +{hidden.length}
          </span>
        </SimpleTooltip>
      )}
    </span>
  );
}

export interface IdentityLinkProps {
  id: string;
  /** Site name of the event; the link needs identity:read on it. */
  site?: string;
  truncate?: number;
}

/** Identity ID linking to the identity detail page (plain ID without identity:read), with copy. */
export function IdentityLink({ id, site, truncate = 0 }: IdentityLinkProps) {
  const { t } = useTranslation('requests');
  const { can } = useAuth();
  if (!id) return <Dash />;
  if (!can(PERMISSIONS.identityRead, site || undefined)) return <IdText value={id} truncate={truncate} />;
  const shown = truncate > 0 && id.length > truncate ? `${id.slice(0, truncate)}…` : id;
  // Same wrapping contract as IdText: untruncated IDs break anywhere so the link
  // never overlaps the field next to it.
  return (
    <span className="inline-flex max-w-full items-start gap-0.5 font-mono text-xs" title={id}>
      <Link
        to="/identities/$id"
        params={{ id }}
        className={cn(
          'text-primary underline-offset-4 hover:underline',
          truncate > 0 ? 'truncate' : 'min-w-0 wrap-anywhere',
        )}
        aria-label={t('shared.openIdentity', { id })}
      >
        {shown}
      </Link>
      <CopyButton value={id} className="-my-1 shrink-0" />
    </span>
  );
}

/** Absolute event time with the relative time in a tooltip (explorer tables need exact order). */
export function EventTime({ value }: { value: TimeInput }) {
  const { i18n } = useTranslation();
  const now = useNow();
  const date = toDate(value);
  if (!date) return <Dash />;
  return (
    <SimpleTooltip content={formatRelative(date, now, i18n.language)}>
      <time dateTime={date.toISOString()} className="tabular text-xs whitespace-nowrap">
        {formatDateTime(date)}
      </time>
    </SimpleTooltip>
  );
}

const FLAG_TONES = {
  suppressed: 'bg-amber-500/10 text-amber-700 dark:text-amber-400',
  late: 'bg-sky-500/10 text-sky-700 dark:text-sky-400',
  probe: 'bg-violet-500/10 text-violet-700 dark:text-violet-400',
} as const;

/** Suppressed / late / probe markers of a request event. */
export function EventFlags({ event }: { event: Pick<RequestEvent, 'suppressed' | 'late' | 'probe'> }) {
  const { t } = useTranslation('requests');
  const flags = (Object.keys(FLAG_TONES) as Array<keyof typeof FLAG_TONES>).filter((flag) => event[flag]);
  if (flags.length === 0) return <Dash />;
  return (
    <span className="inline-flex max-w-full flex-wrap gap-1">
      {flags.map((flag) => (
        <span
          key={flag}
          className={cn('rounded px-1.5 py-0.5 text-[11px] font-medium', FLAG_TONES[flag])}
          title={t(`flags.${flag}Hint`)}
        >
          {t(`flags.${flag}`)}
        </span>
      ))}
    </span>
  );
}
