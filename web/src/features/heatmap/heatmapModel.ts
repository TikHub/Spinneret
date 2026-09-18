import { type GetHeatmapResponse } from '@/gen/spinneret/v1/dashboard_pb';
import { toNumber } from '@/lib/format';

/** Cell metric used for colouring. */
export const HEATMAP_METRICS = ['cooldown', 'score'] as const;
export type HeatmapMetric = (typeof HEATMAP_METRICS)[number];

/** Identity states accepted by GetHeatmap. */
export const HEATMAP_STATES = [
  'pending',
  'active',
  'expired',
  'banned',
  'quarantined',
  'disabled',
  'retired',
] as const;
export const DEFAULT_HEATMAP_STATES: readonly string[] = ['active', 'pending'];

/** Row limits offered by the page (the server caps at 500). */
export const HEATMAP_LIMITS = [100, 250, 500] as const;
export const DEFAULT_HEATMAP_LIMIT = 100;

/** Maximum score of the health model. */
export const MAX_SCORE = 100;

/**
 * Remaining cooldowns at or above this value are permanent bans: the server
 * reports them as the largest int64 (math.MaxInt64 milliseconds).
 */
export const PERMANENT_COOLDOWN_MS = Number.MAX_SAFE_INTEGER;

/**
 * States in which an identity without hot state in a group is leasable. The
 * server sends only cells with hot state; missing cells follow the identity
 * availability, approximated here from the lifecycle state.
 */
const LEASABLE_STATES: ReadonlySet<string> = new Set(['active', 'pending']);

export interface HeatmapRowView {
  identityId: string;
  /** Axis label: the row label or a short identity ID. */
  label: string;
  state: string;
}

export interface HeatmapColumnView {
  name: string;
  id: string;
}

/** One identity × endpoint group cell (dense grid, derived for missing cells). */
export interface HeatmapCellView {
  row: number;
  col: number;
  /** Decayed score; undefined when the server has no hot state (baseline score). */
  score: number | undefined;
  /** Remaining cooldown in ms; 0 for permanent bans (see `permanent`). */
  cooldownMs: number;
  /** True when the identity is banned permanently (no finite cooldown). */
  permanent: boolean;
  available: boolean;
  hasHotState: boolean;
}

export interface HeatmapGrid {
  rows: HeatmapRowView[];
  columns: HeatmapColumnView[];
  /** Cells in row-major order: index = row * columns.length + col. */
  cells: HeatmapCellView[];
  /** Longest finite remaining cooldown (permanent bans excluded). */
  maxCooldownMs: number;
}

/** Short identity label for axes: the last characters of the ID. */
export function shortIdentityLabel(identityId: string, keep = 10): string {
  return identityId.length > keep ? `…${identityId.slice(-keep)}` : identityId;
}

/** Expands the sparse server matrix into a dense grid (out-of-range cells are ignored). */
export function buildHeatmapGrid(
  res: Pick<GetHeatmapResponse, 'rows' | 'columns' | 'columnIds' | 'cells'>,
): HeatmapGrid {
  const rows = res.rows.map((row) => ({
    identityId: row.identityId,
    label: row.label || shortIdentityLabel(row.identityId),
    state: row.state,
  }));
  const columns = res.columns.map((name, i) => ({ name, id: res.columnIds[i] ?? '' }));
  const width = columns.length;
  const cells: HeatmapCellView[] = rows.flatMap((row, r) =>
    columns.map((_, c) => ({
      row: r,
      col: c,
      score: undefined,
      cooldownMs: 0,
      permanent: false,
      available: LEASABLE_STATES.has(row.state),
      hasHotState: false,
    })),
  );
  let maxCooldownMs = 0;
  for (const cell of res.cells) {
    if (cell.row < 0 || cell.row >= rows.length || cell.col < 0 || cell.col >= width) continue;
    const remainingMs = Math.max(0, toNumber(cell.cooldownRemainingMs));
    const permanent = remainingMs >= PERMANENT_COOLDOWN_MS;
    const cooldownMs = permanent ? 0 : remainingMs;
    maxCooldownMs = Math.max(maxCooldownMs, cooldownMs);
    cells[cell.row * width + cell.col] = {
      row: cell.row,
      col: cell.col,
      score: cell.score,
      cooldownMs,
      permanent,
      available: cell.available,
      hasHotState: true,
    };
  }
  return { rows, columns, cells, maxCooldownMs };
}

/** Cell at a position, if any. */
export function cellAt(grid: HeatmapGrid, row: number, col: number): HeatmapCellView | undefined {
  if (col < 0 || col >= grid.columns.length) return undefined;
  return grid.cells[row * grid.columns.length + col];
}

/**
 * Cell categories drawn as separate series: the metric scale (colored by the
 * visual map) and fixed-color categories that must stay distinguishable.
 */
export type HeatmapBucket = 'cooling' | 'permanent' | 'available' | 'unavailable' | 'scored' | 'baseline';

/** ECharts heatmap datum: [column index, row index, value]. */
export type HeatmapDatum = [number, number, number];

export interface HeatmapSeriesData {
  bucket: HeatmapBucket;
  /** True for the series colored by the visual map. */
  scaled: boolean;
  data: HeatmapDatum[];
}

/** Buckets per metric in drawing order; the first one is the scaled series. */
export const METRIC_BUCKETS: Record<HeatmapMetric, readonly HeatmapBucket[]> = {
  cooldown: ['cooling', 'permanent', 'available', 'unavailable'],
  score: ['scored', 'baseline'],
};

function bucketOf(cell: HeatmapCellView, metric: HeatmapMetric): HeatmapBucket {
  if (metric === 'score') return cell.score === undefined ? 'baseline' : 'scored';
  if (cell.permanent) return 'permanent';
  if (cell.cooldownMs > 0) return 'cooling';
  return cell.available ? 'available' : 'unavailable';
}

function valueOf(cell: HeatmapCellView, metric: HeatmapMetric): number {
  if (metric === 'score') return cell.score ?? 0;
  return cell.cooldownMs;
}

/** Splits the grid into one series per bucket of the metric. */
export function heatmapSeriesData(grid: HeatmapGrid, metric: HeatmapMetric): HeatmapSeriesData[] {
  const buckets = METRIC_BUCKETS[metric];
  const byBucket = new Map<HeatmapBucket, HeatmapDatum[]>(buckets.map((b) => [b, []]));
  for (const cell of grid.cells) {
    byBucket.get(bucketOf(cell, metric))?.push([cell.col, cell.row, valueOf(cell, metric)]);
  }
  return buckets.map((bucket, i) => ({ bucket, scaled: i === 0, data: byBucket.get(bucket) ?? [] }));
}

/** Range of the visual map: 0..max remaining cooldown (at least 1 s) or 0..100 score. */
export function visualRange(grid: HeatmapGrid, metric: HeatmapMetric): [number, number] {
  if (metric === 'score') return [0, MAX_SCORE];
  return [0, Math.max(grid.maxCooldownMs, 1000)];
}
