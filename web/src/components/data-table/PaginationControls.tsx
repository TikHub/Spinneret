import { ChevronLeftIcon, ChevronRightIcon, ChevronsLeftIcon } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { Button } from '@/components/ui/button';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { formatNumber } from '@/lib/format';

import { PAGE_SIZE_OPTIONS } from './pagination';
import { type DataTablePagination } from './types';

export interface PaginationControlsProps extends DataTablePagination {
  /** Rows on the current page. */
  rowCount: number;
  disabled?: boolean;
}

/** Cursor pagination footer: first/previous/next, page number, total and page size. */
export function PaginationControls({
  pager,
  nextPageToken,
  total,
  pageSizeOptions = PAGE_SIZE_OPTIONS,
  rowCount,
  disabled,
}: PaginationControlsProps) {
  const { t, i18n } = useTranslation();
  const hasNext = Boolean(nextPageToken);

  return (
    <div className="flex flex-wrap items-center justify-between gap-2 border-t px-3 py-2 text-xs text-muted-foreground">
      <div className="flex items-center gap-3">
        <span>{t('table.page', { page: pager.pageIndex + 1 })}</span>
        <span>{t('table.rowsOnPage', { count: rowCount })}</span>
        {total !== undefined && total > 0 && (
          <span>{t('table.total', { total: formatNumber(total, undefined, i18n.language) })}</span>
        )}
      </div>
      <div className="flex items-center gap-2">
        <span className="hidden sm:inline">{t('table.rowsPerPage')}</span>
        <Select
          value={String(pager.pageSize)}
          onValueChange={(v) => pager.setPageSize(Number(v))}
          disabled={disabled}
        >
          <SelectTrigger size="sm" className="h-7 w-20" aria-label={t('table.rowsPerPage')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {pageSizeOptions.map((size) => (
              <SelectItem key={size} value={String(size)}>
                {size}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Button
          variant="outline"
          size="icon-sm"
          className="size-7"
          onClick={pager.first}
          disabled={disabled || !pager.canPrevious}
          aria-label={t('table.firstPage')}
        >
          <ChevronsLeftIcon />
        </Button>
        <Button
          variant="outline"
          size="icon-sm"
          className="size-7"
          onClick={pager.previous}
          disabled={disabled || !pager.canPrevious}
          aria-label={t('table.previousPage')}
        >
          <ChevronLeftIcon />
        </Button>
        <Button
          variant="outline"
          size="icon-sm"
          className="size-7"
          onClick={() => pager.next(nextPageToken)}
          disabled={disabled || !hasNext}
          aria-label={t('table.nextPage')}
        >
          <ChevronRightIcon />
        </Button>
      </div>
    </div>
  );
}
