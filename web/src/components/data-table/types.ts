import { type ColumnDef, type RowData } from '@tanstack/react-table';

import { type DataTableFeatures } from './features';
import { type CursorPagination } from './pagination';

/** Column definition accepted by DataTable (value type erased so mixed accessor columns fit). */
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export type DataTableColumn<TData extends RowData> = ColumnDef<DataTableFeatures, TData, any>;

/** Server pagination wiring for DataTable. */
export interface DataTablePagination {
  pager: CursorPagination;
  /** `next_page_token` of the current response; empty or undefined when on the last page. */
  nextPageToken?: string;
  /** `total` of the current response, when the API returns it. */
  total?: number;
  pageSizeOptions?: readonly number[];
}
