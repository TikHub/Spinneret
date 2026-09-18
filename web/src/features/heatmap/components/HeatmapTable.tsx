import { Link } from '@tanstack/react-router';
import { useTranslation } from 'react-i18next';

import { StateBadge } from '@/components/StateBadge';
import { cn } from '@/lib/utils';

import { cellText } from '../cellText';
import { cellAt, type HeatmapCellView, type HeatmapGrid, type HeatmapMetric } from '../heatmapModel';

export interface HeatmapTableProps {
  grid: HeatmapGrid;
  metric: HeatmapMetric;
  /** Link identities to their detail page. */
  linkIdentities: boolean;
}

function cellTone(cell: HeatmapCellView, metric: HeatmapMetric): string {
  if (metric === 'score') {
    if (cell.score === undefined) return 'text-muted-foreground';
    if (cell.score < 30) return 'text-rose-700 dark:text-rose-400';
    if (cell.score < 60) return 'text-amber-700 dark:text-amber-400';
    return 'text-emerald-700 dark:text-emerald-400';
  }
  if (cell.permanent) return 'font-medium text-rose-700 dark:text-rose-400';
  if (cell.cooldownMs > 0) return 'text-orange-700 dark:text-orange-400';
  return cell.available ? 'text-emerald-700 dark:text-emerald-400' : 'text-muted-foreground';
}

/** Accessible table alternative of the heatmap (same cells as text). */
export function HeatmapTable({ grid, metric, linkIdentities }: HeatmapTableProps) {
  const { t, i18n } = useTranslation('heatmap');
  return (
    <div className="max-h-[70vh] overflow-auto rounded-lg border bg-card">
      <table className="w-full text-sm">
        <caption className="sr-only">{t('tableCaption', { metric: t(`metrics.${metric}`) })}</caption>
        <thead className="sticky top-0 z-20 bg-muted/90 backdrop-blur">
          <tr className="border-b">
            <th
              scope="col"
              className="sticky left-0 z-10 bg-muted/90 px-3 py-2 text-left text-xs font-medium text-muted-foreground"
            >
              {t('identity')}
            </th>
            {grid.columns.map((column) => (
              <th
                key={column.id || column.name}
                scope="col"
                className="px-3 py-2 text-right font-mono text-xs font-medium whitespace-nowrap text-muted-foreground"
              >
                {column.name}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {grid.rows.map((row, r) => (
            <tr key={row.identityId} className="border-b last:border-0 hover:bg-muted/40">
              <th scope="row" className="sticky left-0 z-10 bg-card px-3 py-1.5 text-left font-normal">
                <span className="flex items-center gap-2 whitespace-nowrap">
                  {linkIdentities ? (
                    <Link
                      to="/identities/$id"
                      params={{ id: row.identityId }}
                      className="font-mono text-xs text-primary underline-offset-4 hover:underline"
                      title={row.identityId}
                    >
                      {row.label}
                    </Link>
                  ) : (
                    <span className="font-mono text-xs" title={row.identityId}>
                      {row.label}
                    </span>
                  )}
                  <StateBadge state={row.state} kind="identity" />
                </span>
              </th>
              {grid.columns.map((column, c) => {
                const cell = cellAt(grid, r, c);
                return (
                  <td
                    key={column.id || column.name}
                    className={cn(
                      'tabular px-3 py-1.5 text-right text-xs whitespace-nowrap',
                      cell && cellTone(cell, metric),
                    )}
                  >
                    {cell ? cellText(cell, metric, t, i18n.language) : '—'}
                  </td>
                );
              })}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
