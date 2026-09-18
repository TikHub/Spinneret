import { useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';

import { useTheme } from '@/app/theme/ThemeProvider';
import { EChart } from '@/components/charts/EChart';
import { humanizeDuration } from '@/lib/duration';

import { tooltipLines } from '../cellText';
import { type HeatmapBucket, type HeatmapGrid, type HeatmapMetric } from '../heatmapModel';
import {
  buildHeatmapOption,
  heatmapChartHeight,
  positionFromParams,
  zoomFromEvent,
  type ZoomWindow,
} from '../heatmapOption';

/** Delay before a dragged slider window is kept (avoids re-rendering on every drag frame). */
const ZOOM_COMMIT_MS = 250;

export interface HeatmapChartProps {
  grid: HeatmapGrid;
  metric: HeatmapMetric;
  /** Called with the identity of a clicked cell; omit to disable drill-down. */
  onOpenIdentity?: (identityId: string) => void;
}

/** ECharts heatmap of identities × endpoint groups with drill-down on click. */
export function HeatmapChart({ grid, metric, onOpenIdentity }: HeatmapChartProps) {
  const { t, i18n } = useTranslation('heatmap');
  const lng = i18n.language;
  const { resolvedTheme } = useTheme();
  // Slider window per row count, so auto refresh does not scroll back to the top.
  const [zoom, setZoom] = useState<{ rows: number; window: ZoomWindow } | undefined>(undefined);
  const zoomTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(zoomTimer.current), []);
  const rowCount = grid.rows.length;
  const zoomWindow = zoom?.rows === rowCount ? zoom.window : undefined;

  const option = useMemo(
    () =>
      buildHeatmapOption({
        grid,
        metric,
        zoom: zoomWindow,
        theme: resolvedTheme,
        bucketLabel: (bucket: HeatmapBucket) => t(`buckets.${bucket}`),
        formatScale: (value) =>
          metric === 'score' ? String(Math.round(value)) : humanizeDuration(Math.max(0, value), { t }),
        tooltipLines: (cell, row, column) => tooltipLines(cell, row, column, t, lng),
      }),
    [grid, metric, zoomWindow, resolvedTheme, t, lng],
  );

  const onEvents = useMemo(
    () => ({
      click: (params: unknown) => {
        const pos = positionFromParams(params);
        const row = pos ? grid.rows[pos.row] : undefined;
        if (row && onOpenIdentity) onOpenIdentity(row.identityId);
      },
      datazoom: (params: unknown) => {
        const next = zoomFromEvent(params);
        if (!next) return;
        clearTimeout(zoomTimer.current);
        zoomTimer.current = setTimeout(() => setZoom({ rows: rowCount, window: next }), ZOOM_COMMIT_MS);
      },
    }),
    [grid, rowCount, onOpenIdentity],
  );

  return (
    <EChart
      option={option}
      onEvents={onEvents}
      height={heatmapChartHeight(grid.rows.length, grid.columns.length)}
      aria-label={t('chartLabel', { rows: grid.rows.length, columns: grid.columns.length })}
    />
  );
}
