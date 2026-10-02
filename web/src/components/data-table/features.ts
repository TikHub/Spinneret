import {
  columnVisibilityFeature,
  createSortedRowModel,
  metaHelper,
  rowExpandingFeature,
  rowSelectionFeature,
  rowSortingFeature,
  sortFn_alphanumeric,
  sortFn_basic,
  sortFn_datetime,
  sortFn_text,
  tableFeatures,
} from '@tanstack/react-table';

/** Column metadata read by DataTable and the column visibility menu. */
export interface DataTableColumnMeta {
  /** Human label used by the column visibility menu (defaults to the column id). */
  label?: string;
  /** Cell and header alignment. */
  align?: 'left' | 'center' | 'right';
  /** Extra classes for body cells. */
  className?: string;
  /** Extra classes for the header cell. */
  headerClassName?: string;
}

/**
 * TanStack Table features of the console tables: a table only has the APIs of
 * the features registered here. The sort functions are the built-ins that
 * `sortFn: 'auto'` picks from; unregistered, every client-sorted column would
 * fall back to the case-sensitive `basic` comparison.
 */
export const dataTableFeatures = tableFeatures({
  columnVisibilityFeature,
  rowExpandingFeature,
  rowSelectionFeature,
  rowSortingFeature,
  sortedRowModel: createSortedRowModel(),
  sortFns: {
    alphanumeric: sortFn_alphanumeric,
    basic: sortFn_basic,
    datetime: sortFn_datetime,
    text: sortFn_text,
  },
  columnMeta: metaHelper<DataTableColumnMeta>(),
});

export type DataTableFeatures = typeof dataTableFeatures;
