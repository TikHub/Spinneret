import { ChevronLeftIcon, ChevronRightIcon, ChevronsLeftIcon } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { type CursorPagination } from '@/components/data-table';
import { Button } from '@/components/ui/button';
import { formatNumber } from '@/lib/format';

export interface HeatmapPagerProps {
  pager: CursorPagination;
  nextPageToken: string | undefined;
  rowCount: number;
  limit: number;
  total: number | undefined;
  disabled?: boolean;
}

/** Cursor paging over identity rows: "rows 101–200 of 1,234" with first/previous/next. */
export function HeatmapPager({ pager, nextPageToken, rowCount, limit, total, disabled }: HeatmapPagerProps) {
  const { t, i18n } = useTranslation('heatmap');
  const lng = i18n.language;
  const from = rowCount === 0 ? 0 : pager.pageIndex * limit + 1;
  const to = pager.pageIndex * limit + rowCount;

  return (
    <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground">
      <span className="tabular">
        {t('rowsRange', {
          from: formatNumber(from, undefined, lng),
          to: formatNumber(to, undefined, lng),
          total: formatNumber(total ?? to, undefined, lng),
        })}
      </span>
      <div className="flex items-center gap-2">
        <Button
          variant="outline"
          size="icon-sm"
          className="size-7"
          onClick={pager.first}
          disabled={disabled || !pager.canPrevious}
          aria-label={t('common:table.firstPage')}
        >
          <ChevronsLeftIcon />
        </Button>
        <Button
          variant="outline"
          size="icon-sm"
          className="size-7"
          onClick={pager.previous}
          disabled={disabled || !pager.canPrevious}
          aria-label={t('common:table.previousPage')}
        >
          <ChevronLeftIcon />
        </Button>
        <span className="tabular">{t('common:table.page', { page: pager.pageIndex + 1 })}</span>
        <Button
          variant="outline"
          size="icon-sm"
          className="size-7"
          onClick={() => pager.next(nextPageToken)}
          disabled={disabled || !nextPageToken}
          aria-label={t('common:table.nextPage')}
        >
          <ChevronRightIcon />
        </Button>
      </div>
    </div>
  );
}
