import { flexRender, getCoreRowModel, useReactTable, type VisibilityState } from '@tanstack/react-table';
import { ChevronRightIcon, LoaderCircleIcon, RefreshCwIcon, TriangleAlertIcon } from 'lucide-react';
import { Fragment, useMemo, useState, type MouseEvent } from 'react';
import { useTranslation } from 'react-i18next';

import { ColumnVisibilityMenu, PaginationControls, type DataTablePagination } from '@/components/data-table';
import { EmptyState } from '@/components/EmptyState';
import { ErrorState } from '@/components/ErrorState';
import { Button } from '@/components/ui/button';
import { Skeleton } from '@/components/ui/skeleton';
import { type RiskEvent } from '@/gen/spinneret/v1/dashboard_pb';
import { errorMessage } from '@/lib/errors';
import { cn } from '@/lib/utils';

import { RISK_HIDDEN_COLUMNS, riskColumns } from './riskColumns';
import { RiskEventDetail } from './RiskEventDetail';

const SKELETON_ROWS = 8;
const INTERACTIVE_SELECTOR = 'a, button, input, [role="menuitem"]';

export interface RiskEventsTableProps {
  events: readonly RiskEvent[] | undefined;
  isLoading: boolean;
  isFetching: boolean;
  error: unknown;
  onRetry: () => void;
  pagination: DataTablePagination;
}

/** Server-paginated risk event table whose rows expand to show request details. */
export function RiskEventsTable({
  events,
  isLoading,
  isFetching,
  error,
  onRetry,
  pagination,
}: RiskEventsTableProps) {
  const { t } = useTranslation('risk-events');
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(new Set());
  const [visibility, setVisibility] = useState<VisibilityState>(RISK_HIDDEN_COLUMNS);
  const columns = useMemo(() => riskColumns(t), [t]);
  const rows = useMemo(() => [...(events ?? [])], [events]);

  // eslint-disable-next-line react-hooks/incompatible-library
  const table = useReactTable<RiskEvent>({
    data: rows,
    columns,
    getRowId: (event) => event.id,
    state: { columnVisibility: visibility },
    onColumnVisibilityChange: setVisibility,
    getCoreRowModel: getCoreRowModel(),
  });

  const toggle = (id: string) =>
    setExpanded((current) => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });

  const onRowClick = (id: string, event: MouseEvent<HTMLTableRowElement>) => {
    if (event.target instanceof Element && event.target.closest(INTERACTIVE_SELECTOR)) return;
    if (window.getSelection()?.toString()) return;
    toggle(id);
  };

  const colSpan = table.getVisibleLeafColumns().length + 1;
  const hasError = error !== undefined && error !== null;
  const showSkeleton = isLoading && rows.length === 0 && !hasError;

  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center justify-end gap-2">
        {isFetching && !showSkeleton && (
          <LoaderCircleIcon
            className="size-4 animate-spin text-muted-foreground"
            aria-label={t('common:table.loading')}
          />
        )}
        <ColumnVisibilityMenu table={table} />
      </div>
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
          <table className="w-full text-sm">
            <thead className="sticky top-0 z-10 bg-muted/80 backdrop-blur supports-[backdrop-filter]:bg-muted/60">
              {table.getHeaderGroups().map((group) => (
                <tr key={group.id} className="border-b">
                  <th scope="col" className="w-8 px-2">
                    <span className="sr-only">{t('expand')}</span>
                  </th>
                  {group.headers.map((header) => (
                    <th
                      key={header.id}
                      scope="col"
                      className={cn(
                        'h-9 px-3 text-left align-middle text-xs font-medium whitespace-nowrap text-muted-foreground',
                        header.column.columnDef.meta?.align === 'right' && 'text-right',
                      )}
                    >
                      {header.isPlaceholder
                        ? null
                        : flexRender(header.column.columnDef.header, header.getContext())}
                    </th>
                  ))}
                </tr>
              ))}
            </thead>
            <tbody>
              {showSkeleton &&
                Array.from({ length: SKELETON_ROWS }, (_, i) => (
                  <tr key={`skeleton-${i}`} className="border-b last:border-0">
                    {Array.from({ length: colSpan }, (_, j) => (
                      <td key={j} className="px-3 py-2.5">
                        <Skeleton className="h-4 w-full max-w-32" />
                      </td>
                    ))}
                  </tr>
                ))}
              {hasError && rows.length === 0 && (
                <tr>
                  <td colSpan={colSpan}>
                    <ErrorState error={error} onRetry={onRetry} compact />
                  </td>
                </tr>
              )}
              {!showSkeleton && !hasError && rows.length === 0 && (
                <tr>
                  <td colSpan={colSpan}>
                    <EmptyState compact title={t('empty.title')} description={t('empty.description')} />
                  </td>
                </tr>
              )}
              {table.getRowModel().rows.map((row) => {
                const open = expanded.has(row.id);
                const detailId = `risk-event-detail-${row.id}`;
                return (
                  <Fragment key={row.id}>
                    <tr
                      className={cn(
                        'cursor-pointer border-b transition-colors last:border-0 hover:bg-muted/50',
                        open && 'border-b-0 bg-muted/30',
                      )}
                      onClick={(event) => onRowClick(row.id, event)}
                    >
                      <td className="w-8 px-2 align-middle">
                        <Button
                          variant="ghost"
                          size="icon-sm"
                          className="size-6"
                          aria-expanded={open}
                          aria-controls={open ? detailId : undefined}
                          aria-label={open ? t('collapseRow') : t('expandRow')}
                          onClick={() => toggle(row.id)}
                        >
                          <ChevronRightIcon
                            className={cn('size-4 transition-transform', open && 'rotate-90')}
                          />
                        </Button>
                      </td>
                      {row.getVisibleCells().map((cell) => (
                        <td
                          key={cell.id}
                          className={cn(
                            'px-3 py-2 align-middle whitespace-nowrap',
                            cell.column.columnDef.meta?.align === 'right' && 'text-right',
                            cell.column.columnDef.meta?.className,
                          )}
                        >
                          {flexRender(cell.column.columnDef.cell, cell.getContext())}
                        </td>
                      ))}
                    </tr>
                    {open && (
                      <tr id={detailId} className="border-b bg-muted/30 last:border-0">
                        <td colSpan={colSpan} className="px-11 pt-1 pb-3">
                          <RiskEventDetail event={row.original} />
                        </td>
                      </tr>
                    )}
                  </Fragment>
                );
              })}
            </tbody>
          </table>
        </div>
        <PaginationControls {...pagination} rowCount={rows.length} disabled={showSkeleton} />
      </div>
    </div>
  );
}
