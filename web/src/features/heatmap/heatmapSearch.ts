import {
  compactSearch,
  readEnum,
  readInt,
  readList,
  readString,
  type SearchRecord,
} from '@/features/requests/shared/searchParams';

import {
  DEFAULT_HEATMAP_LIMIT,
  DEFAULT_HEATMAP_STATES,
  HEATMAP_LIMITS,
  HEATMAP_METRICS,
  HEATMAP_STATES,
  type HeatmapMetric,
} from './heatmapModel';

export const HEATMAP_VIEWS = ['chart', 'table'] as const;
export type HeatmapView = (typeof HEATMAP_VIEWS)[number];

/** URL value meaning "every state except retired" (an empty state filter on the server). */
export const ALL_STATES = 'all';

/** Heatmap selection mirrored in the URL. */
export interface HeatmapSelection {
  /** Site name; empty until chosen (the page picks the first site). */
  site: string;
  /** Client type; empty until chosen (the page picks the site's first client). */
  client: string;
  metric: HeatmapMetric;
  /** Identity states; empty = all except retired. */
  states: string[];
  limit: number;
  view: HeatmapView;
}

export const DEFAULT_HEATMAP_SELECTION: HeatmapSelection = {
  site: '',
  client: '',
  metric: 'cooldown',
  states: [...DEFAULT_HEATMAP_STATES],
  limit: DEFAULT_HEATMAP_LIMIT,
  view: 'chart',
};

function sameList(a: readonly string[], b: readonly string[]): boolean {
  return a.length === b.length && a.every((v, i) => v === b[i]);
}

/** Keeps known states in canonical order. */
export function normalizeStates(states: readonly string[]): string[] {
  return HEATMAP_STATES.filter((s) => states.includes(s));
}

/** Reads the state filter: "all" = every state except retired; missing or only unknown states = default. */
function readStates(search: SearchRecord): string[] {
  const raw = readList(search, 'states');
  if (raw.includes(ALL_STATES)) return [];
  const states = normalizeStates(raw);
  return states.length > 0 ? states : [...DEFAULT_HEATMAP_STATES];
}

/** URL keys: site client metric states limit view. */
export function parseHeatmapSearch(search: SearchRecord): HeatmapSelection {
  const limit = readInt(search, 'limit', 1, 500);
  return {
    site: readString(search, 'site', 64),
    client: readString(search, 'client', 32),
    metric: readEnum(search, 'metric', HEATMAP_METRICS, DEFAULT_HEATMAP_SELECTION.metric),
    states: readStates(search),
    limit:
      limit !== undefined && (HEATMAP_LIMITS as readonly number[]).includes(limit)
        ? limit
        : DEFAULT_HEATMAP_LIMIT,
    view: readEnum(search, 'view', HEATMAP_VIEWS, DEFAULT_HEATMAP_SELECTION.view),
  };
}

/** Search params of a selection; defaults are omitted. */
export function heatmapSelectionToSearch(selection: HeatmapSelection): SearchRecord {
  const states = normalizeStates(selection.states);
  return compactSearch({
    site: selection.site,
    client: selection.client,
    metric: selection.metric === DEFAULT_HEATMAP_SELECTION.metric ? undefined : selection.metric,
    states:
      states.length === 0
        ? ALL_STATES
        : sameList(states, normalizeStates(DEFAULT_HEATMAP_STATES))
          ? undefined
          : states.join(','),
    limit: selection.limit === DEFAULT_HEATMAP_LIMIT ? undefined : selection.limit,
    view: selection.view === DEFAULT_HEATMAP_SELECTION.view ? undefined : selection.view,
  });
}
