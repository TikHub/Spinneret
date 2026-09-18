import {
  flexRender,
  getCoreRowModel,
  getSortedRowModel,
  useReactTable,
  type ExpandedState,
  type Header,
  type OnChangeFn,
  type Row,
  type RowSelectionState,
  type SortingState,
  type VisibilityState,
} from '@tanstack/react-table';
import { useVirtualizer } from '@tanstack/react-virtual';
import {
  ArrowDownIcon,
  ArrowUpDownIcon,
  ArrowUpIcon,
  LoaderCircleIcon,
  RefreshCwIcon,
  TriangleAlertIcon,
} from 'lucide-react';
import {
  useCallback,
  useId,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
  type MouseEvent,
  type ReactNode,
} from 'react';
import { useTranslation } from 'react-i18next';

import { EmptyState } from '@/components/EmptyState';
import { ErrorState } from '@/components/ErrorState';
import { Button } from '@/components/ui/button';
import { Checkbox } from '@/components/ui/checkbox';
import { Skeleton } from '@/components/ui/skeleton';
import { errorMessage } from '@/lib/errors';
import { cn } from '@/lib/utils';

import { ColumnVisibilityMenu } from './ColumnVisibilityMenu';
import { detailRowId, EXPAND_COLUMN_ID, expanderColumn, toDisplayRows } from './expansion';
import { PaginationControls } from './PaginationControls';
import { type DataTableColumn, type DataTablePagination } from './types';

/** Row count above which `virtualize="auto"` turns virtualization on. */
export const AUTO_VIRTUALIZE_THRESHOLD = 100;
const DEFAULT_ROW_HEIGHT = 37;
const SELECT_COLUMN_ID = '__select';
/** Estimated height of an expanded detail row before it is measured (virtualized tables). */
const DEFAULT_EXPANDED_ROW_HEIGHT = 160;

export interface DataTableProps<TData> {
  columns: readonly DataTableColumn<TData>[];
  data: readonly TData[] | undefined;
  /** Stable row id (recommended: entity ID); required for selection across refetches. */
  getRowId?: (row: TData, index: number) => string;

  /** Initial load without data: shows skeleton rows. */
  isLoading?: boolean;
  /** Background refetch indicator. */
  isFetching?: boolean;
  /** Query error: shows ErrorState (with retry when onRetry is set). */
  error?: unknown;
  onRetry?: () => void;

  emptyTitle?: ReactNode;
  emptyDescription?: ReactNode;
  emptyAction?: ReactNode;

  /** Controlled sorting; with manualSorting the server sorts (map it to order_by). */
  sorting?: SortingState;
  onSortingChange?: OnChangeFn<SortingState>;
  manualSorting?: boolean;
  initialSorting?: SortingState;

  /** Show the column visibility menu (default true). */
  enableColumnVisibility?: boolean;
  columnVisibility?: VisibilityState;
  onColumnVisibilityChange?: OnChangeFn<VisibilityState>;
  initialColumnVisibility?: VisibilityState;

  /** Adds a checkbox column. */
  enableRowSelection?: boolean | ((row: Row<TData>) => boolean);
  rowSelection?: RowSelectionState;
  onRowSelectionChange?: OnChangeFn<RowSelectionState>;
  /** Bulk action slot rendered while rows are selected. */
  bulkActions?: (selectedRows: TData[], clearSelection: () => void) => ReactNode;

  /**
   * Row detail panel: adds an expander column and renders the returned content
   * in a full-width row below each expanded row.
   */
  renderExpandedRow?: (row: TData, tableRow: Row<TData>) => ReactNode;
  /** Rows that get an expander (default: every row when renderExpandedRow is set). */
  getRowCanExpand?: (row: Row<TData>) => boolean;
  /** Controlled expansion state keyed by row id (pass getRowId to keep it across refetches). */
  expanded?: ExpandedState;
  onExpandedChange?: OnChangeFn<ExpandedState>;
  initialExpanded?: ExpandedState;

  /** Makes rows clickable (e.g. open the detail page). */
  onRowClick?: (row: TData) => void;
  rowClassName?: (row: TData) => string | undefined;

  /** Virtualize rows: true, false or "auto" (more than 100 rows). */
  virtualize?: boolean | 'auto';
  estimateRowHeight?: number;
  /** Max height of the scroll area; defaults to 70vh when virtualized. */
  maxHeight?: number | string;

  /** Server cursor pagination footer. */
  pagination?: DataTablePagination;
  /** Left side of the toolbar (filters); the column menu sits on the right. */
  toolbar?: ReactNode;
  className?: string;
  skeletonRows?: number;
}

function SortIndicator<TData>({ header }: { header: Header<TData, unknown> }) {
  const sorted = header.column.getIsSorted();
  if (sorted === 'asc') return <ArrowUpIcon className="size-3.5" />;
  if (sorted === 'desc') return <ArrowDownIcon className="size-3.5" />;
  return <ArrowUpDownIcon className="size-3.5 opacity-40" />;
}

/** Elements inside a row that handle their own clicks and keys (row activation ignores them). */
const INTERACTIVE_SELECTOR =
  'a, button, input, select, textarea, label, [role="checkbox"], [role="switch"], [role="menuitem"], [role="combobox"], [contenteditable="true"]';

/** True when the row must not be activated by this event. */
function isFromInteractiveElement(target: EventTarget | null, row: Element): boolean {
  if (!(target instanceof Element)) return false;
  // A dropdown menu, popover or dialog opened from a cell renders into a portal
  // outside the row, but React still bubbles its events to the row that rendered
  // it. Such events belong to the overlay, never to the row.
  if (!row.contains(target)) return true;
  const interactive = target.closest(INTERACTIVE_SELECTOR);
  return interactive !== null && interactive !== row;
}

function hasTextSelection(): boolean {
  const selection = typeof window === 'undefined' ? null : window.getSelection();
  return selection !== null && !selection.isCollapsed && selection.toString().trim() !== '';
}

/** Checkbox and expander columns: fixed narrow width, no right padding. */
function isNarrowColumn(id: string): boolean {
  return id === SELECT_COLUMN_ID || id === EXPAND_COLUMN_ID;
}

function alignClass(align: 'left' | 'center' | 'right' | undefined): string | undefined {
  if (align === 'right') return 'text-right';
  if (align === 'center') return 'text-center';
  return undefined;
}

/**
 * Data table built on TanStack Table + Virtual: client or server sorting,
 * column visibility, row selection with a bulk action slot, virtualization
 * for large pages, cursor pagination and loading/empty/error states.
 */
export function DataTable<TData>({
  columns,
  data,
  getRowId,
  isLoading = false,
  isFetching = false,
  error,
  onRetry,
  emptyTitle,
  emptyDescription,
  emptyAction,
  sorting: sortingProp,
  onSortingChange,
  manualSorting = false,
  initialSorting = [],
  enableColumnVisibility = true,
  columnVisibility: visibilityProp,
  onColumnVisibilityChange,
  initialColumnVisibility = {},
  enableRowSelection = false,
  rowSelection: selectionProp,
  onRowSelectionChange,
  bulkActions,
  renderExpandedRow,
  getRowCanExpand,
  expanded: expandedProp,
  onExpandedChange,
  initialExpanded = {},
  onRowClick,
  rowClassName,
  virtualize = 'auto',
  estimateRowHeight = DEFAULT_ROW_HEIGHT,
  maxHeight,
  pagination,
  toolbar,
  className,
  skeletonRows = 8,
}: DataTableProps<TData>) {
  const { t } = useTranslation();
  const [sortingState, setSortingState] = useState<SortingState>(initialSorting);
  const [visibilityState, setVisibilityState] = useState<VisibilityState>(initialColumnVisibility);
  const [selectionState, setSelectionState] = useState<RowSelectionState>({});
  const [expandedState, setExpandedState] = useState<ExpandedState>(initialExpanded);
  const tableId = useId();
  const expandable = renderExpandedRow !== undefined;

  const sorting = sortingProp ?? sortingState;
  const columnVisibility = visibilityProp ?? visibilityState;
  const rowSelection = selectionProp ?? selectionState;
  const expanded = expandedProp ?? expandedState;
  const rows = useMemo(() => (data ?? []) as TData[], [data]);

  const allColumns = useMemo<DataTableColumn<TData>[]>(() => {
    const leading: DataTableColumn<TData>[] = [];
    if (expandable) {
      leading.push(
        expanderColumn(tableId, {
          header: t('table.rowDetails'),
          expand: t('table.expandRow'),
          collapse: t('table.collapseRow'),
        }),
      );
    }
    if (!enableRowSelection) return [...leading, ...columns];
    const selectColumn: DataTableColumn<TData> = {
      id: SELECT_COLUMN_ID,
      enableSorting: false,
      enableHiding: false,
      size: 32,
      header: ({ table }) => (
        <Checkbox
          checked={
            table.getIsAllPageRowsSelected()
              ? true
              : table.getIsSomePageRowsSelected()
                ? 'indeterminate'
                : false
          }
          onCheckedChange={(value) => table.toggleAllPageRowsSelected(Boolean(value))}
          aria-label={t('table.selectAll')}
        />
      ),
      cell: ({ row }) => (
        <Checkbox
          checked={row.getIsSelected()}
          disabled={!row.getCanSelect()}
          onCheckedChange={(value) => row.toggleSelected(Boolean(value))}
          onClick={(event) => event.stopPropagation()}
          aria-label={t('table.selectRow')}
        />
      ),
    };
    return [selectColumn, ...leading, ...columns];
  }, [columns, enableRowSelection, expandable, tableId, t]);

  // eslint-disable-next-line react-hooks/incompatible-library
  const table = useReactTable<TData>({
    data: rows,
    columns: allColumns,
    getRowId,
    state: { sorting, columnVisibility, rowSelection, expanded },
    onSortingChange: onSortingChange ?? setSortingState,
    onColumnVisibilityChange: onColumnVisibilityChange ?? setVisibilityState,
    onRowSelectionChange: onRowSelectionChange ?? setSelectionState,
    onExpandedChange: onExpandedChange ?? setExpandedState,
    // Detail panels, not sub-rows: rows never flatten and expansion survives data changes.
    getRowCanExpand: expandable ? (getRowCanExpand ?? (() => true)) : () => false,
    autoResetExpanded: false,
    enableRowSelection,
    manualSorting,
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: manualSorting ? undefined : getSortedRowModel(),
  });

  const tableRows = table.getRowModel().rows;
  const selectedRows = table.getSelectedRowModel().rows;
  const shouldVirtualize = virtualize === 'auto' ? tableRows.length > AUTO_VIRTUALIZE_THRESHOLD : virtualize;

  // `expanded` is read through row.getIsExpanded(); it is listed so toggles recompute the rows.
  const displayRows = useMemo(
    () => toDisplayRows(tableRows, expandable),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [tableRows, expandable, expanded],
  );
  const scrollRef = useRef<HTMLDivElement>(null);
  // Keys keep measured detail-row heights attached to their rows when rows expand or collapse.
  const getItemKey = useCallback((index: number) => displayRows[index]?.key ?? index, [displayRows]);
  const estimateSize = useCallback(
    (index: number) =>
      displayRows[index]?.kind === 'detail' ? DEFAULT_EXPANDED_ROW_HEIGHT : estimateRowHeight,
    [displayRows, estimateRowHeight],
  );
  const virtualizer = useVirtualizer({
    count: shouldVirtualize ? displayRows.length : 0,
    getScrollElement: () => scrollRef.current,
    estimateSize,
    getItemKey,
    overscan: 12,
  });
  const virtualItems = shouldVirtualize ? virtualizer.getVirtualItems() : [];
  const paddingTop = shouldVirtualize && virtualItems.length > 0 ? (virtualItems[0]?.start ?? 0) : 0;
  const paddingBottom =
    shouldVirtualize && virtualItems.length > 0
      ? virtualizer.getTotalSize() - (virtualItems[virtualItems.length - 1]?.end ?? 0)
      : 0;
  const visibleRows = shouldVirtualize
    ? virtualItems.flatMap((item) => {
        const displayRow = displayRows[item.index];
        return displayRow ? [{ displayRow, index: item.index }] : [];
      })
    : displayRows.map((displayRow, index) => ({ displayRow, index }));

  const visibleColumnCount = table.getVisibleLeafColumns().length;
  const hasError = error !== undefined && error !== null;
  const showSkeleton = isLoading && rows.length === 0 && !hasError;
  const showError = hasError && rows.length === 0;
  // A failed background refetch keeps the previous rows; say that they may be stale.
  const showStaleError = hasError && rows.length > 0;
  const showEmpty = !showSkeleton && !showError && tableRows.length === 0;
  const clearSelection = () => table.resetRowSelection();

  const activateRow = (row: TData, event: MouseEvent<HTMLTableRowElement>) => {
    if (!onRowClick || isFromInteractiveElement(event.target, event.currentTarget) || hasTextSelection()) {
      return;
    }
    onRowClick(row);
  };

  const onRowKeyDown = (row: TData, event: KeyboardEvent<HTMLTableRowElement>) => {
    if (!onRowClick || event.target !== event.currentTarget) return;
    if (event.key === 'Enter' || event.key === ' ') {
      event.preventDefault();
      onRowClick(row);
    }
  };

  const hasToolbar = toolbar || enableColumnVisibility || (bulkActions && selectedRows.length > 0);

  return (
    <div className={cn('flex flex-col gap-2', className)}>
      {hasToolbar && (
        <div className="flex flex-wrap items-center gap-2">
          <div className="flex min-w-0 flex-1 flex-wrap items-center gap-2">{toolbar}</div>
          {isFetching && !showSkeleton && (
            <LoaderCircleIcon
              className="size-4 animate-spin text-muted-foreground"
              aria-label={t('table.loading')}
            />
          )}
          {enableColumnVisibility && <ColumnVisibilityMenu table={table} />}
        </div>
      )}
      {bulkActions && selectedRows.length > 0 && (
        <div className="flex flex-wrap items-center gap-2 rounded-md border border-primary/30 bg-primary/5 px-3 py-1.5 text-sm">
          <span className="font-medium">{t('table.selected', { count: selectedRows.length })}</span>
          <div className="flex flex-wrap items-center gap-2">
            {bulkActions(
              selectedRows.map((row) => row.original),
              clearSelection,
            )}
          </div>
          <Button variant="ghost" size="sm" className="ml-auto" onClick={clearSelection}>
            {t('table.clearSelection')}
          </Button>
        </div>
      )}
      {showStaleError && (
        <div
          role="alert"
          className="flex flex-wrap items-center gap-2 rounded-md border border-destructive/30 bg-destructive/5 px-3 py-1.5 text-sm text-destructive"
        >
          <TriangleAlertIcon className="size-4 shrink-0" aria-hidden />
          <span className="min-w-0 flex-1 break-words">
            {t('table.refreshFailed')} {errorMessage(error, t)}
          </span>
          {onRetry && (
            <Button variant="outline" size="sm" className="h-7" onClick={onRetry}>
              <RefreshCwIcon />
              {t('actions.retry')}
            </Button>
          )}
        </div>
      )}
      <div className="overflow-hidden rounded-lg border bg-card">
        <div
          ref={scrollRef}
          className="relative overflow-auto"
          style={{ maxHeight: maxHeight ?? (shouldVirtualize ? '70vh' : undefined) }}
        >
          <table className="w-full caption-bottom text-sm">
            <thead className="sticky top-0 z-10 bg-muted/80 backdrop-blur supports-[backdrop-filter]:bg-muted/60">
              {table.getHeaderGroups().map((headerGroup) => (
                <tr key={headerGroup.id} className="border-b">
                  {headerGroup.headers.map((header) => {
                    const meta = header.column.columnDef.meta;
                    const canSort = header.column.getCanSort();
                    const sorted = header.column.getIsSorted();
                    return (
                      <th
                        key={header.id}
                        colSpan={header.colSpan}
                        scope="col"
                        aria-sort={
                          sorted === 'asc' ? 'ascending' : sorted === 'desc' ? 'descending' : undefined
                        }
                        className={cn(
                          'h-9 px-3 text-left align-middle text-xs font-medium whitespace-nowrap text-muted-foreground',
                          isNarrowColumn(header.column.id) && 'w-8 pr-0',
                          alignClass(meta?.align),
                          meta?.headerClassName,
                        )}
                      >
                        {header.isPlaceholder ? null : canSort ? (
                          <button
                            type="button"
                            className={cn(
                              'inline-flex items-center gap-1 rounded hover:text-foreground',
                              meta?.align === 'right' && 'flex-row-reverse',
                            )}
                            onClick={header.column.getToggleSortingHandler()}
                          >
                            {flexRender(header.column.columnDef.header, header.getContext())}
                            <SortIndicator header={header} />
                          </button>
                        ) : (
                          flexRender(header.column.columnDef.header, header.getContext())
                        )}
                      </th>
                    );
                  })}
                </tr>
              ))}
            </thead>
            <tbody>
              {showSkeleton &&
                Array.from({ length: skeletonRows }, (_, i) => (
                  <tr key={`skeleton-${i}`} className="border-b last:border-0">
                    {Array.from({ length: visibleColumnCount }, (_, j) => (
                      <td key={j} className="px-3 py-2.5">
                        <Skeleton className="h-4 w-full max-w-40" />
                      </td>
                    ))}
                  </tr>
                ))}
              {showError && (
                <tr>
                  <td colSpan={visibleColumnCount}>
                    <ErrorState error={error} onRetry={onRetry} compact />
                  </td>
                </tr>
              )}
              {showEmpty && (
                <tr>
                  <td colSpan={visibleColumnCount}>
                    <EmptyState
                      compact
                      title={emptyTitle ?? t('table.noResults')}
                      description={emptyDescription ?? t('table.noResultsDescription')}
                      action={emptyAction}
                    />
                  </td>
                </tr>
              )}
              {paddingTop > 0 && (
                <tr aria-hidden>
                  <td colSpan={visibleColumnCount} style={{ height: paddingTop, padding: 0 }} />
                </tr>
              )}
              {visibleRows.map(({ displayRow, index }) => {
                const { row } = displayRow;
                if (displayRow.kind === 'detail') {
                  return (
                    <tr
                      key={displayRow.key}
                      id={detailRowId(tableId, row.id)}
                      data-index={index}
                      ref={shouldVirtualize ? virtualizer.measureElement : undefined}
                      className="border-b bg-muted/20 last:border-0"
                    >
                      <td colSpan={visibleColumnCount} className="px-3 py-3 align-top">
                        {renderExpandedRow?.(row.original, row)}
                      </td>
                    </tr>
                  );
                }
                return (
                  <tr
                    key={row.id}
                    data-state={row.getIsSelected() ? 'selected' : undefined}
                    className={cn(
                      'border-b transition-colors last:border-0 hover:bg-muted/50 data-[state=selected]:bg-primary/5',
                      onRowClick &&
                        'cursor-pointer outline-none focus-visible:bg-muted/50 focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset',
                      rowClassName?.(row.original),
                    )}
                    style={shouldVirtualize ? { height: estimateRowHeight } : undefined}
                    tabIndex={onRowClick ? 0 : undefined}
                    onClick={onRowClick ? (event) => activateRow(row.original, event) : undefined}
                    onKeyDown={onRowClick ? (event) => onRowKeyDown(row.original, event) : undefined}
                  >
                    {row.getVisibleCells().map((cell) => {
                      const meta = cell.column.columnDef.meta;
                      return (
                        <td
                          key={cell.id}
                          className={cn(
                            'px-3 py-2 align-middle whitespace-nowrap',
                            isNarrowColumn(cell.column.id) && 'w-8 pr-0',
                            alignClass(meta?.align),
                            meta?.className,
                          )}
                        >
                          {flexRender(cell.column.columnDef.cell, cell.getContext())}
                        </td>
                      );
                    })}
                  </tr>
                );
              })}
              {paddingBottom > 0 && (
                <tr aria-hidden>
                  <td colSpan={visibleColumnCount} style={{ height: paddingBottom, padding: 0 }} />
                </tr>
              )}
            </tbody>
          </table>
        </div>
        {pagination && <PaginationControls {...pagination} rowCount={rows.length} disabled={showSkeleton} />}
      </div>
    </div>
  );
}
