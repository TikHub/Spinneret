import { type ResolvedTheme } from '@/app/theme/ThemeProvider';
import { type EChartsOption } from '@/components/charts/EChart';

import {
  cellAt,
  heatmapSeriesData,
  visualRange,
  type HeatmapBucket,
  type HeatmapCellView,
  type HeatmapColumnView,
  type HeatmapGrid,
  type HeatmapMetric,
  type HeatmapRowView,
} from './heatmapModel';

/** Colors of the heatmap per theme (hex for the canvas renderer). */
export interface HeatmapPalette {
  /** Sequential single-hue ramp for remaining cooldown (low → high). */
  cooling: readonly string[];
  /** Diverging ramp for the score: red (0) → neutral → green (100). */
  score: readonly string[];
  /** Permanent bans (beyond the cooldown scale). */
  permanent: string;
  available: string;
  unavailable: string;
  baseline: string;
  /** Card surface, used as the gap between cells. */
  surface: string;
  emphasis: string;
}

export const HEATMAP_PALETTE: Record<ResolvedTheme, HeatmapPalette> = {
  light: {
    cooling: ['#fed7aa', '#fb923c', '#ea580c', '#9a3412'],
    score: ['#e11d48', '#e2e8f0', '#059669'],
    permanent: '#4c0519',
    available: '#6ee7b7',
    unavailable: '#94a3b8',
    baseline: '#f1f5f9',
    surface: '#ffffff',
    emphasis: '#0f172a',
  },
  dark: {
    cooling: ['#431407', '#9a3412', '#f97316', '#fdba74'],
    score: ['#fb7185', '#334155', '#34d399'],
    permanent: '#e11d48',
    available: '#047857',
    unavailable: '#475569',
    baseline: '#1e293b',
    surface: '#0f172a',
    emphasis: '#f8fafc',
  },
};

/** Rows shown at once before the vertical slider appears. */
export const MAX_VISIBLE_ROWS = 40;
const ROW_HEIGHT_PX = 18;
const CHROME_HEIGHT_PX = 130;
/** Columns above which x labels are rotated. */
const ROTATE_LABELS_ABOVE = 8;

export interface HeatmapTooltipLine {
  label: string;
  value: string;
}

/** Visible window of the row slider in percent (kept across refreshes). */
export interface ZoomWindow {
  start: number;
  end: number;
}

export interface HeatmapOptionInput {
  grid: HeatmapGrid;
  metric: HeatmapMetric;
  theme: ResolvedTheme;
  /** Row slider window; defaults to the first MAX_VISIBLE_ROWS rows. */
  zoom?: ZoomWindow;
  bucketLabel: (bucket: HeatmapBucket) => string;
  /** Formats the ends of the visual map (e.g. humanized cooldown). */
  formatScale: (value: number) => string;
  /** Tooltip lines of a cell; values are escaped by the builder. */
  tooltipLines: (
    cell: HeatmapCellView,
    row: HeatmapRowView,
    column: HeatmapColumnView,
  ) => HeatmapTooltipLine[];
}

const HTML_ESCAPES: Record<string, string> = {
  '&': '&amp;',
  '<': '&lt;',
  '>': '&gt;',
  '"': '&quot;',
  "'": '&#39;',
};

/** Escapes text for the HTML tooltip (labels come from server data). */
export function escapeHtml(text: string): string {
  return text.replace(/[&<>"']/g, (ch) => HTML_ESCAPES[ch] ?? ch);
}

/** Chart height for a row count (the slider takes over beyond MAX_VISIBLE_ROWS). */
export function heatmapChartHeight(rowCount: number, columnCount: number): number {
  const visible = Math.max(1, Math.min(rowCount, MAX_VISIBLE_ROWS));
  const labels = columnCount > ROTATE_LABELS_ABOVE ? 60 : 0;
  return visible * ROW_HEIGHT_PX + CHROME_HEIGHT_PX + labels;
}

/** Reads the percent window of a `datazoom` event (direct or batched). */
export function zoomFromEvent(params: unknown): ZoomWindow | undefined {
  if (params === null || typeof params !== 'object') return undefined;
  const { batch } = params as { batch?: unknown };
  const source: unknown = Array.isArray(batch) ? batch[0] : params;
  if (source === null || typeof source !== 'object') return undefined;
  const { start, end } = source as { start?: unknown; end?: unknown };
  return typeof start === 'number' && typeof end === 'number' ? { start, end } : undefined;
}

/** Reads [col, row] from an ECharts event or tooltip parameter. */
export function positionFromParams(params: unknown): { row: number; col: number } | undefined {
  if (params === null || typeof params !== 'object') return undefined;
  const value = (params as { value?: unknown }).value;
  if (!Array.isArray(value)) return undefined;
  const [col, row] = value;
  return typeof col === 'number' && typeof row === 'number' ? { row, col } : undefined;
}

function tooltipHtml(input: HeatmapOptionInput, params: unknown): string {
  const pos = positionFromParams(params);
  if (!pos) return '';
  const cell = cellAt(input.grid, pos.row, pos.col);
  const row = input.grid.rows[pos.row];
  const column = input.grid.columns[pos.col];
  if (!cell || !row || !column) return '';
  return input
    .tooltipLines(cell, row, column)
    .map(
      (line) =>
        `<div style="display:flex;justify-content:space-between;gap:16px"><span style="opacity:.7">${escapeHtml(
          line.label,
        )}</span><span style="font-weight:600">${escapeHtml(line.value)}</span></div>`,
    )
    .join('');
}

/** ECharts option: identities (rows) × endpoint groups (columns), one series per cell bucket. */
export function buildHeatmapOption(input: HeatmapOptionInput): EChartsOption {
  const { grid, metric, theme } = input;
  const palette = HEATMAP_PALETTE[theme];
  const series = heatmapSeriesData(grid, metric);
  const [min, max] = visualRange(grid, metric);
  const fixedColors: Partial<Record<HeatmapBucket, string>> = {
    permanent: palette.permanent,
    available: palette.available,
    unavailable: palette.unavailable,
    baseline: palette.baseline,
  };
  const rotate = grid.columns.length > ROTATE_LABELS_ABOVE;
  const zoom = grid.rows.length > MAX_VISIBLE_ROWS;

  return {
    animation: false,
    grid: { left: 8, right: zoom ? 36 : 16, top: rotate ? 80 : 28, bottom: 48, containLabel: true },
    tooltip: {
      trigger: 'item',
      confine: true,
      formatter: (params: unknown) => tooltipHtml(input, params),
    },
    xAxis: {
      type: 'category',
      position: 'top',
      data: grid.columns.map((c) => c.name),
      splitArea: { show: false },
      axisTick: { show: false },
      axisLabel: { interval: 0, rotate: rotate ? 35 : 0, fontSize: 11 },
    },
    yAxis: {
      type: 'category',
      inverse: true,
      data: grid.rows.map((r) => r.label),
      axisTick: { show: false },
      axisLabel: { fontSize: 10, fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace' },
    },
    // The scaled series is explained by the visible ramp; every other series gets
    // a hidden constant map, because ECharts requires a visual map per heatmap
    // series on a cartesian grid (it throws in development builds without one).
    visualMap: [
      {
        type: 'continuous',
        seriesIndex: 0,
        dimension: 2,
        min,
        max,
        calculable: false,
        orient: 'horizontal',
        left: 0,
        bottom: 0,
        itemWidth: 12,
        itemHeight: 140,
        text: [input.formatScale(max), input.formatScale(min)],
        inRange: { color: [...(metric === 'score' ? palette.score : palette.cooling)] },
      },
      ...series.flatMap((s, index) => {
        if (s.scaled) return [];
        const color = fixedColors[s.bucket] ?? palette.baseline;
        const values = s.data.map((datum) => datum[2]);
        const low = values.length > 0 ? Math.min(...values) : 0;
        const high = values.length > 0 ? Math.max(...values) : low;
        return [
          {
            type: 'continuous' as const,
            show: false,
            seriesIndex: index,
            dimension: 2,
            min: low,
            max: high > low ? high : low + 1,
            calculable: false,
            inRange: { color: [color, color] },
            outOfRange: { color: [color], opacity: 1 },
          },
        ];
      }),
    ],
    legend: {
      bottom: 0,
      right: 0,
      selectedMode: false,
      itemWidth: 12,
      itemHeight: 12,
      // Fixed-color categories present on this page (the scaled series is explained by the visual map).
      data: series.filter((s) => !s.scaled && s.data.length > 0).map((s) => input.bucketLabel(s.bucket)),
    },
    dataZoom: zoom
      ? [
          {
            type: 'slider',
            yAxisIndex: 0,
            right: 4,
            width: 14,
            ...(input.zoom
              ? { start: input.zoom.start, end: input.zoom.end }
              : { startValue: 0, endValue: MAX_VISIBLE_ROWS - 1 }),
            filterMode: 'none',
            showDetail: false,
            brushSelect: false,
          },
          {
            type: 'inside',
            yAxisIndex: 0,
            filterMode: 'none',
            zoomOnMouseWheel: false,
            moveOnMouseWheel: true,
          },
        ]
      : [],
    series: series.map((s) => ({
      type: 'heatmap',
      name: input.bucketLabel(s.bucket),
      data: s.data,
      itemStyle: {
        borderColor: palette.surface,
        borderWidth: 1,
        ...(s.scaled ? {} : { color: fixedColors[s.bucket] }),
      },
      emphasis: { itemStyle: { borderColor: palette.emphasis, borderWidth: 1 } },
    })),
  };
}
