import { type ColumnDef, type RowData } from '@tanstack/react-table';

import { type CursorPagination } from './pagination';

declare module '@tanstack/react-table' {
  // Declaration merging requires the same type parameters as the library.
  // eslint-disable-next-line @typescript-eslint/no-unused-vars
  interface ColumnMeta<TData extends RowData, TValue> {
    /** Human label used by the column visibility menu (defaults to the column id). */
    label?: string;
    /** Cell and header alignment. */
    align?: 'left' | 'center' | 'right';
    /** Extra classes for body cells. */
    className?: string;
    /** Extra classes for the header cell. */
    headerClassName?: string;
  }
}

/** Column definition accepted by DataTable (value type erased so mixed accessor columns fit). */
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export type DataTableColumn<TData> = ColumnDef<TData, any>;

/** Server pagination wiring for DataTable. */
export interface DataTablePagination {
  pager: CursorPagination;
  /** `next_page_token` of the current response; empty or undefined when on the last page. */
  nextPageToken?: string;
  /** `total` of the current response, when the API returns it. */
  total?: number;
  pageSizeOptions?: readonly number[];
}
