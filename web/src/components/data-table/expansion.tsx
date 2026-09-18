import { type Row } from '@tanstack/react-table';
import { ChevronRightIcon } from 'lucide-react';

import { Button } from '@/components/ui/button';
import { cn } from '@/lib/utils';

import { type DataTableColumn } from './types';

/** Column id of the expander column added by DataTable when rows can expand. */
export const EXPAND_COLUMN_ID = '__expand';

/** One rendered table row: a data row or the detail row of an expanded data row. */
export type DisplayRow<TData> = { kind: 'row'; key: string; row: Row<TData> } | DetailDisplayRow<TData>;

interface DetailDisplayRow<TData> {
  kind: 'detail';
  key: string;
  row: Row<TData>;
}

/** DOM id of the detail row of a data row (target of the expander's aria-controls). */
export function detailRowId(tableId: string, rowId: string): string {
  return `${tableId}-details-${encodeURIComponent(rowId)}`;
}

/** Data rows interleaved with the detail rows of expanded rows, in display order. */
export function toDisplayRows<TData>(rows: readonly Row<TData>[], expandable: boolean): DisplayRow<TData>[] {
  const out: DisplayRow<TData>[] = [];
  for (const row of rows) {
    out.push({ kind: 'row', key: row.id, row });
    if (expandable && row.getCanExpand() && row.getIsExpanded()) {
      out.push({ kind: 'detail', key: `${row.id}:details`, row });
    }
  }
  return out;
}

export interface ExpanderLabels {
  /** Screen-reader header of the expander column. */
  header: string;
  expand: string;
  collapse: string;
}

/** Expander column: a toggle button on every row that can expand. */
export function expanderColumn<TData>(tableId: string, labels: ExpanderLabels): DataTableColumn<TData> {
  return {
    id: EXPAND_COLUMN_ID,
    enableSorting: false,
    enableHiding: false,
    size: 32,
    header: () => <span className="sr-only">{labels.header}</span>,
    cell: ({ row }) => {
      if (!row.getCanExpand()) return null;
      const expanded = row.getIsExpanded();
      return (
        <Button
          variant="ghost"
          size="icon-sm"
          className="size-6"
          aria-expanded={expanded}
          aria-controls={detailRowId(tableId, row.id)}
          aria-label={expanded ? labels.collapse : labels.expand}
          onClick={(event) => {
            event.stopPropagation();
            row.toggleExpanded();
          }}
        >
          <ChevronRightIcon className={cn('transition-transform', expanded && 'rotate-90')} />
        </Button>
      );
    },
  };
}
