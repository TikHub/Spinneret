import { describe, expect, it } from 'vitest';

import {
  DEFAULT_HEATMAP_SELECTION,
  heatmapSelectionToSearch,
  parseHeatmapSearch,
  type HeatmapSelection,
} from './heatmapSearch';

describe('heatmap selection <-> URL', () => {
  it('defaults to cooldown, active + pending, 100 rows and the chart', () => {
    expect(parseHeatmapSearch({})).toEqual(DEFAULT_HEATMAP_SELECTION);
    expect(DEFAULT_HEATMAP_SELECTION.states).toEqual(['active', 'pending']);
    expect(heatmapSelectionToSearch(DEFAULT_HEATMAP_SELECTION)).toEqual({});
    expect(heatmapSelectionToSearch({ ...DEFAULT_HEATMAP_SELECTION, states: ['pending', 'active'] })).toEqual(
      {},
    );
  });

  it('round-trips a full selection', () => {
    const selection: HeatmapSelection = {
      site: 'shop',
      client: 'app',
      metric: 'score',
      states: ['active', 'banned'],
      limit: 500,
      view: 'table',
    };
    const search = heatmapSelectionToSearch(selection);
    expect(search).toEqual({
      site: 'shop',
      client: 'app',
      metric: 'score',
      states: 'active,banned',
      limit: 500,
      view: 'table',
    });
    expect(parseHeatmapSearch(search)).toEqual(selection);
  });

  it('encodes an empty state filter as "all"', () => {
    const search = heatmapSelectionToSearch({ ...DEFAULT_HEATMAP_SELECTION, states: [] });
    expect(search).toEqual({ states: 'all' });
    expect(parseHeatmapSearch(search).states).toEqual([]);
  });

  it('drops invalid values', () => {
    const selection = parseHeatmapSearch({ metric: 'heat', states: ['bogus'], limit: 42, view: 'grid' });
    expect(selection.metric).toBe('cooldown');
    // Unknown states fall back to the default instead of widening the filter to every state.
    expect(selection.states).toEqual(['active', 'pending']);
    expect(parseHeatmapSearch({ states: 'banned,bogus' }).states).toEqual(['banned']);
    expect(selection.limit).toBe(100);
    expect(selection.view).toBe('chart');
    expect(parseHeatmapSearch({ limit: '250' }).limit).toBe(250);
  });
});
