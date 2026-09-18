import { RefreshCwIcon, ScrollTextIcon, TriangleAlertIcon } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';

import { PaginationControls, type DataTablePagination } from '@/components/data-table';
import { EmptyState } from '@/components/EmptyState';
import { ErrorState } from '@/components/ErrorState';
import { Button } from '@/components/ui/button';
import { Skeleton } from '@/components/ui/skeleton';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { type AuditLog } from '@/gen/spinneret/v1/access_admin_pb';
import { errorMessage } from '@/lib/errors';

import { AUDIT_COLUMN_COUNT, AuditLogRow } from './AuditLogRow';

const SKELETON_ROWS = 8;

export interface AuditTableProps {
  logs: readonly AuditLog[] | undefined;
  isLoading: boolean;
  error: unknown;
  onRetry: () => void;
  pagination: DataTablePagination;
}

/**
 * Audit log table with expandable rows (details JSON). DataTable has no row
 * expansion, so this table renders the shared table primitives with the same
 * loading, empty, error and pagination behavior.
 */
export function AuditTable({ logs, isLoading, error, onRetry, pagination }: AuditTableProps) {
  const { t } = useTranslation('access');
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(new Set());
  const rows = logs ?? [];
  const hasError = error !== null && error !== undefined;
  const showSkeleton = isLoading && rows.length === 0 && !hasError;

  const toggle = (id: string) =>
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });

  return (
    <div className="grid gap-2">
      {hasError && rows.length > 0 && (
        <div
          role="alert"
          className="flex flex-wrap items-center gap-2 rounded-md border border-destructive/30 bg-destructive/5 px-3 py-1.5 text-sm text-destructive"
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
      <div className="overflow-hidden rounded-lg border bg-card">
        <div className="relative max-h-[70vh] overflow-auto">
          <Table>
            <TableHeader className="sticky top-0 z-10 bg-muted/80 backdrop-blur supports-[backdrop-filter]:bg-muted/60">
              <TableRow className="hover:bg-transparent">
                <TableHead className="w-8 pr-0">
                  <span className="sr-only">{t('audit.columns.expand')}</span>
                </TableHead>
                <TableHead>{t('audit.columns.time')}</TableHead>
                <TableHead>{t('audit.columns.actor')}</TableHead>
                <TableHead>{t('audit.columns.action')}</TableHead>
                <TableHead>{t('audit.columns.resource')}</TableHead>
                <TableHead>{t('audit.columns.result')}</TableHead>
                <TableHead>{t('audit.columns.namespace')}</TableHead>
                <TableHead>{t('audit.columns.client')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {showSkeleton &&
                Array.from({ length: SKELETON_ROWS }, (_, i) => (
                  <TableRow key={`skeleton-${i}`}>
                    {Array.from({ length: AUDIT_COLUMN_COUNT }, (_, j) => (
                      <TableCell key={j} className="py-2.5">
                        <Skeleton className="h-4 w-full max-w-32" />
                      </TableCell>
                    ))}
                  </TableRow>
                ))}
              {hasError && rows.length === 0 && (
                <TableRow className="hover:bg-transparent">
                  <TableCell colSpan={AUDIT_COLUMN_COUNT}>
                    <ErrorState error={error} onRetry={onRetry} compact />
                  </TableCell>
                </TableRow>
              )}
              {!showSkeleton && !hasError && rows.length === 0 && (
                <TableRow className="hover:bg-transparent">
                  <TableCell colSpan={AUDIT_COLUMN_COUNT} className="whitespace-normal">
                    <EmptyState
                      compact
                      icon={ScrollTextIcon}
                      title={t('audit.empty')}
                      description={t('audit.emptyDescription')}
                    />
                  </TableCell>
                </TableRow>
              )}
              {rows.map((log) => (
                <AuditLogRow
                  key={log.id}
                  log={log}
                  expanded={expanded.has(log.id)}
                  onToggle={() => toggle(log.id)}
                />
              ))}
            </TableBody>
          </Table>
        </div>
        <PaginationControls {...pagination} rowCount={rows.length} disabled={showSkeleton} />
      </div>
    </div>
  );
}
