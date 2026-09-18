import { ChevronDownIcon, ChevronRightIcon } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { IdText } from '@/components/CopyButton';
import { JsonView } from '@/components/JsonView';
import { StateBadge } from '@/components/StateBadge';
import { TimeAgo } from '@/components/TimeAgo';
import { Badge } from '@/components/ui/badge';
import { TableCell, TableRow } from '@/components/ui/table';
import { SimpleTooltip } from '@/components/ui/tooltip';
import { type AuditLog } from '@/gen/spinneret/v1/access_admin_pb';
import { formatDateTime } from '@/lib/time';
import { cn } from '@/lib/utils';

/** StateBadge state used for the color of each result. */
const RESULT_TONE_STATE: Record<string, string> = {
  ok: 'active',
  denied: 'expired',
  error: 'banned',
};

/** Number of columns of the audit table (including the expand column). */
export const AUDIT_COLUMN_COUNT = 8;

export interface AuditLogRowProps {
  log: AuditLog;
  expanded: boolean;
  onToggle: () => void;
}

/** One audit entry plus its expandable details row. */
export function AuditLogRow({ log, expanded, onToggle }: AuditLogRowProps) {
  const { t } = useTranslation('access');
  const detailsId = `audit-details-${log.id}`;
  return (
    <>
      <TableRow
        className={cn('cursor-pointer', expanded && 'border-b-0 bg-muted/30')}
        onClick={(event) => {
          if (event.target instanceof Element && event.target.closest('button, a')) return;
          onToggle();
        }}
      >
        <TableCell className="w-8 pr-0">
          <button
            type="button"
            className="flex size-6 items-center justify-center rounded text-muted-foreground hover:bg-muted hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
            aria-expanded={expanded}
            aria-controls={expanded ? detailsId : undefined}
            aria-label={expanded ? t('audit.hideDetails') : t('audit.showDetails')}
            onClick={onToggle}
          >
            {expanded ? <ChevronDownIcon className="size-4" /> : <ChevronRightIcon className="size-4" />}
          </button>
        </TableCell>
        <TableCell>
          <div className="grid">
            <span className="text-xs tabular">{formatDateTime(log.createdAt)}</span>
            <TimeAgo value={log.createdAt} past className="text-xs text-muted-foreground" />
          </div>
        </TableCell>
        <TableCell className="max-w-48">
          <SimpleTooltip content={log.actorId || undefined}>
            <span className="flex min-w-0 items-center gap-1.5">
              <Badge variant="outline" className="px-1.5 py-0">
                {t(`audit.actorKinds.${log.actorKind}`, { defaultValue: log.actorKind || '—' })}
              </Badge>
              <span className="truncate">{log.actorName || log.actorId || '—'}</span>
            </span>
          </SimpleTooltip>
        </TableCell>
        <TableCell>
          <code className="font-mono text-xs">{log.action}</code>
        </TableCell>
        <TableCell className="max-w-64">
          <span className="grid min-w-0">
            <span className="truncate">{log.resourceName || log.resourceId || '—'}</span>
            <span className="truncate text-xs text-muted-foreground">
              {log.resourceKind}
              {log.resourceName && log.resourceId && (
                <span className="font-mono" title={log.resourceId}>
                  {' · '}
                  {log.resourceId}
                </span>
              )}
            </span>
          </span>
        </TableCell>
        <TableCell>
          <StateBadge
            kind="identity"
            state={RESULT_TONE_STATE[log.result] ?? 'unknown'}
            label={t(`audit.results.${log.result}`, { defaultValue: log.result || '—' })}
          />
        </TableCell>
        <TableCell>
          {log.namespace ? (
            <span className="font-mono text-xs">{log.namespace}</span>
          ) : (
            <span className="text-xs text-muted-foreground">{t('audit.tenantLevel')}</span>
          )}
        </TableCell>
        <TableCell className="max-w-56">
          <span className="grid min-w-0">
            <span className="font-mono text-xs">{log.ip || '—'}</span>
            {log.userAgent && (
              <span className="truncate text-xs text-muted-foreground" title={log.userAgent}>
                {log.userAgent}
              </span>
            )}
          </span>
        </TableCell>
      </TableRow>
      {expanded && (
        <TableRow id={detailsId} className="bg-muted/30 hover:bg-muted/30">
          <TableCell colSpan={AUDIT_COLUMN_COUNT} className="px-4 pt-0 pb-3 whitespace-normal">
            <AuditDetails log={log} />
          </TableCell>
        </TableRow>
      )}
    </>
  );
}

function AuditDetails({ log }: { log: AuditLog }) {
  const { t } = useTranslation('access');
  const hasDetails = log.details !== undefined && Object.keys(log.details).length > 0;
  return (
    <div className="grid gap-3 lg:grid-cols-[minmax(16rem,22rem)_1fr]">
      <dl className="grid h-fit grid-cols-[7rem_1fr] gap-x-2 gap-y-1 text-xs">
        <dt className="text-muted-foreground">{t('audit.details.id')}</dt>
        <dd>
          <IdText value={log.id} />
        </dd>
        <dt className="text-muted-foreground">{t('audit.details.time')}</dt>
        <dd className="tabular">{formatDateTime(log.createdAt)}</dd>
        <dt className="text-muted-foreground">{t('audit.details.actorId')}</dt>
        <dd>
          <IdText value={log.actorId} />
        </dd>
        <dt className="text-muted-foreground">{t('audit.details.resourceKind')}</dt>
        <dd>{log.resourceKind || '—'}</dd>
        <dt className="text-muted-foreground">{t('audit.details.resourceId')}</dt>
        <dd>
          <IdText value={log.resourceId} />
        </dd>
        <dt className="text-muted-foreground">{t('audit.details.userAgent')}</dt>
        <dd className="break-all">{log.userAgent || '—'}</dd>
      </dl>
      <div className="grid content-start gap-1">
        <span className="text-xs text-muted-foreground">{t('audit.details.payload')}</span>
        {hasDetails ? (
          <JsonView value={log.details} collapseDepth={3} className="max-h-80 overflow-auto bg-background" />
        ) : (
          <p className="text-xs text-muted-foreground">{t('audit.details.none')}</p>
        )}
      </div>
    </div>
  );
}
