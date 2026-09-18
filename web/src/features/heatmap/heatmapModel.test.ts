import { create } from '@bufbuild/protobuf';
import { describe, expect, it } from 'vitest';

import {
  GetHeatmapResponseSchema,
  HeatmapCellSchema,
  HeatmapRowSchema,
} from '@/gen/spinneret/v1/dashboard_pb';

import { buildHeatmapGrid, cellAt, heatmapSeriesData, shortIdentityLabel, visualRange } from './heatmapModel';
import {
  buildHeatmapOption,
  escapeHtml,
  heatmapChartHeight,
  HEATMAP_PALETTE,
  MAX_VISIBLE_ROWS,
  positionFromParams,
  zoomFromEvent,
} from './heatmapOption';

function response() {
  return create(GetHeatmapResponseSchema, {
    metric: 'cooldown',
    columns: ['search', 'detail'],
    columnIds: ['eg_s', 'eg_d'],
    rows: [
      create(HeatmapRowSchema, { identityId: 'idn_aaaaaaaaaaaaaaaa', label: 'acct-1', state: 'active' }),
      create(HeatmapRowSchema, { identityId: 'idn_bbbbbbbbbbbbbbbb', label: '', state: 'banned' }),
    ],
    cells: [
      create(HeatmapCellSchema, {
        row: 0,
        col: 0,
        score: 42.5,
        cooldownRemainingMs: 90_000n,
        available: false,
      }),
      create(HeatmapCellSchema, { row: 0, col: 1, score: 88, cooldownRemainingMs: 0n, available: true }),
      create(HeatmapCellSchema, { row: 1, col: 1, score: 12, cooldownRemainingMs: 0n, available: false }),
      // Out of range cells are ignored.
      create(HeatmapCellSchema, { row: 5, col: 0, score: 1, cooldownRemainingMs: 1n }),
    ],
  });
}

describe('buildHeatmapGrid', () => {
  it('expands the sparse matrix into a dense grid', () => {
    const grid = buildHeatmapGrid(response());
    expect(grid.columns).toEqual([
      { name: 'search', id: 'eg_s' },
      { name: 'detail', id: 'eg_d' },
    ]);
    expect(grid.rows.map((r) => r.label)).toEqual(['acct-1', shortIdentityLabel('idn_bbbbbbbbbbbbbbbb')]);
    expect(grid.cells).toHaveLength(4);
    expect(grid.maxCooldownMs).toBe(90_000);
    expect(cellAt(grid, 0, 0)).toMatchObject({ score: 42.5, cooldownMs: 90_000, hasHotState: true });
    // Missing cell of a banned identity: no hot state, no cooldown, not leasable.
    expect(cellAt(grid, 1, 0)).toMatchObject({
      score: undefined,
      cooldownMs: 0,
      available: false,
      hasHotState: false,
    });
    expect(cellAt(grid, 0, 2)).toBeUndefined();
  });

  it('treats the int64 maximum as a permanent ban outside the cooldown scale', () => {
    const grid = buildHeatmapGrid(
      create(GetHeatmapResponseSchema, {
        columns: ['a', 'b'],
        columnIds: ['eg_a', 'eg_b'],
        rows: [create(HeatmapRowSchema, { identityId: 'idn_1', state: 'banned' })],
        cells: [
          create(HeatmapCellSchema, { row: 0, col: 0, score: 5, cooldownRemainingMs: 9223372036854775807n }),
          create(HeatmapCellSchema, { row: 0, col: 1, score: 5, cooldownRemainingMs: 60_000n }),
        ],
      }),
    );
    expect(cellAt(grid, 0, 0)).toMatchObject({ permanent: true, cooldownMs: 0 });
    expect(cellAt(grid, 0, 1)).toMatchObject({ permanent: false, cooldownMs: 60_000 });
    expect(grid.maxCooldownMs).toBe(60_000);
    const series = heatmapSeriesData(grid, 'cooldown');
    expect(series.find((s) => s.bucket === 'permanent')?.data).toEqual([[0, 0, 0]]);
    expect(series.find((s) => s.bucket === 'cooling')?.data).toEqual([[1, 0, 60_000]]);
    expect(visualRange(grid, 'cooldown')).toEqual([0, 60_000]);
    const option = buildHeatmapOption({
      grid,
      metric: 'cooldown',
      theme: 'dark',
      bucketLabel: String,
      formatScale: String,
      tooltipLines: () => [],
    });
    const styled = option.series as Array<{ name: string; itemStyle: { color?: string } }>;
    expect(styled.find((s) => s.name === 'permanent')?.itemStyle.color).toBe(HEATMAP_PALETTE.dark.permanent);
    // The legend lists only fixed-color buckets present on the page.
    expect(option.legend).toMatchObject({ data: ['permanent'] });

    // ECharts requires a visual map per heatmap series on a cartesian grid and
    // throws "Heatmap must use with visualMap" without one, so every series is
    // covered: the scaled one by the visible ramp, the rest by a hidden map that
    // keeps their fixed color.
    const maps = option.visualMap as Array<{ seriesIndex: number; show?: boolean }>;
    expect(maps.map((m) => m.seriesIndex).sort((a, b) => a - b)).toEqual(styled.map((_, index) => index));
    expect(maps.filter((m) => m.show !== false)).toHaveLength(1);
  });

  it('derives missing cells from the identity state', () => {
    const grid = buildHeatmapGrid(
      create(GetHeatmapResponseSchema, {
        columns: ['a'],
        columnIds: ['eg_a'],
        rows: [create(HeatmapRowSchema, { identityId: 'idn_1', state: 'pending' })],
      }),
    );
    expect(cellAt(grid, 0, 0)).toMatchObject({ available: true, hasHotState: false });
  });
});

describe('heatmapSeriesData', () => {
  it('splits cooldown cells into cooling, permanent, available and unavailable', () => {
    const series = heatmapSeriesData(buildHeatmapGrid(response()), 'cooldown');
    expect(series.map((s) => [s.bucket, s.scaled])).toEqual([
      ['cooling', true],
      ['permanent', false],
      ['available', false],
      ['unavailable', false],
    ]);
    expect(series[0]?.data).toEqual([[0, 0, 90_000]]);
    expect(series[1]?.data).toEqual([]);
    expect(series[2]?.data).toEqual([[1, 0, 0]]);
    expect(series[3]?.data).toEqual([
      [0, 1, 0],
      [1, 1, 0],
    ]);
  });

  it('splits score cells into scored and baseline', () => {
    const grid = buildHeatmapGrid(response());
    const series = heatmapSeriesData(grid, 'score');
    expect(series.map((s) => s.bucket)).toEqual(['scored', 'baseline']);
    expect(series[0]?.data).toEqual([
      [0, 0, 42.5],
      [1, 0, 88],
      [1, 1, 12],
    ]);
    expect(series[1]?.data).toEqual([[0, 1, 0]]);
    expect(visualRange(grid, 'score')).toEqual([0, 100]);
    expect(visualRange(grid, 'cooldown')).toEqual([0, 90_000]);
    expect(visualRange(buildHeatmapGrid(create(GetHeatmapResponseSchema, {})), 'cooldown')).toEqual([
      0, 1000,
    ]);
  });
});

describe('buildHeatmapOption', () => {
  const grid = buildHeatmapGrid(response());
  const input = {
    grid,
    theme: 'light' as const,
    bucketLabel: (b: string) => `label:${b}`,
    formatScale: (v: number) => `${v}`,
    tooltipLines: (_cell: unknown, row: { label: string }) => [{ label: 'identity', value: row.label }],
  };

  it('maps buckets to heatmap series with a visual map on the scaled series', () => {
    const option = buildHeatmapOption({ ...input, metric: 'cooldown' });
    const series = option.series as Array<{ type: string; name: string; itemStyle: { color?: string } }>;
    expect(series.map((s) => [s.type, s.name])).toEqual([
      ['heatmap', 'label:cooling'],
      ['heatmap', 'label:permanent'],
      ['heatmap', 'label:available'],
      ['heatmap', 'label:unavailable'],
    ]);
    expect(series[0]?.itemStyle.color).toBeUndefined();
    expect(series[2]?.itemStyle.color).toBe(HEATMAP_PALETTE.light.available);
    // The first visual map is the visible ramp of the scaled series.
    expect(Array.isArray(option.visualMap) ? option.visualMap[0] : undefined).toMatchObject({
      seriesIndex: 0,
      dimension: 2,
      min: 0,
      max: 90_000,
    });
    expect(option.legend).toMatchObject({ data: ['label:available', 'label:unavailable'] });
    expect(option.yAxis).toMatchObject({ inverse: true, data: grid.rows.map((r) => r.label) });
    expect(option.xAxis).toMatchObject({ data: ['search', 'detail'] });
    expect(option.dataZoom).toEqual([]);
  });

  it('uses the score scale and escapes tooltip content', () => {
    const option = buildHeatmapOption({
      ...input,
      metric: 'score',
      tooltipLines: () => [{ label: 'id', value: '<img src=x onerror=alert(1)>' }],
    });
    expect(Array.isArray(option.visualMap) ? option.visualMap[0] : undefined).toMatchObject({
      min: 0,
      max: 100,
      inRange: { color: [...HEATMAP_PALETTE.light.score] },
    });
    const tooltip = option.tooltip as { formatter: (p: unknown) => string };
    const html = tooltip.formatter({ value: [1, 0, 88] });
    expect(html).toContain('&lt;img');
    expect(html).not.toContain('<img');
    expect(tooltip.formatter({ value: [9, 9, 0] })).toBe('');
  });

  it('adds a vertical slider for many rows', () => {
    const many = {
      ...grid,
      rows: Array.from({ length: MAX_VISIBLE_ROWS + 1 }, (_, i) => ({
        identityId: `idn_${i}`,
        label: `r${i}`,
        state: 'active',
      })),
    };
    const option = buildHeatmapOption({ ...input, grid: many, metric: 'cooldown' });
    expect(option.dataZoom).toHaveLength(2);
    expect(heatmapChartHeight(500, 2)).toBe(heatmapChartHeight(MAX_VISIBLE_ROWS, 2));
    expect(heatmapChartHeight(3, 20)).toBeGreaterThan(heatmapChartHeight(3, 2));
  });
});

describe('helpers', () => {
  it('reads positions from chart params and escapes html', () => {
    expect(positionFromParams({ value: [2, 3, 10] })).toEqual({ col: 2, row: 3 });
    expect(positionFromParams({ value: 'x' })).toBeUndefined();
    expect(positionFromParams(null)).toBeUndefined();
    expect(escapeHtml(`a&b<"'>`)).toBe('a&amp;b&lt;&quot;&#39;&gt;');
  });
});

describe('zoomFromEvent', () => {
  it('reads direct and batched datazoom events', () => {
    expect(zoomFromEvent({ start: 10, end: 30 })).toEqual({ start: 10, end: 30 });
    expect(zoomFromEvent({ batch: [{ start: 5, end: 25 }] })).toEqual({ start: 5, end: 25 });
    expect(zoomFromEvent({ startValue: 1 })).toBeUndefined();
    const option = buildHeatmapOption({
      grid: {
        rows: Array.from({ length: 50 }, (_, i) => ({ identityId: `i${i}`, label: `${i}`, state: 'active' })),
        columns: [],
        cells: [],
        maxCooldownMs: 0,
      },
      metric: 'cooldown',
      theme: 'dark',
      zoom: { start: 20, end: 60 },
      bucketLabel: String,
      formatScale: String,
      tooltipLines: () => [],
    });
    expect((option.dataZoom as Array<Record<string, unknown>>)[0]).toMatchObject({ start: 20, end: 60 });
  });
});
