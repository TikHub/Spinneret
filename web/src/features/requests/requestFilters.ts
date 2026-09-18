import { type QueryRequestEventsRequest } from '@/gen/spinneret/v1/dashboard_pb';

import { isOutcome, type Outcome } from './shared/outcomes';
import {
  compactSearch,
  parseIntInput,
  readInt,
  readList,
  readString,
  type SearchRecord,
} from './shared/searchParams';
import {
  presetSelection,
  readTimeRange,
  timeRangeToSearch,
  toTimeRange,
  type TimeRangePreset,
  type TimeRangeSelection,
} from './shared/timeRange';

/** Default window of the request explorer (the server default is also one hour). */
export const DEFAULT_REQUEST_RANGE: TimeRangePreset = '1h';
export const MAX_HTTP_STATUS = 999;
/** Upper bound for the minimum latency filter (1 hour). */
export const MAX_MIN_LATENCY_MS = 3_600_000;

/** Filters of the request explorer, mirrored in the URL. */
export interface RequestFilters {
  range: TimeRangeSelection;
  /** Site name. */
  site: string;
  /** Endpoint group ID (requires a site in the UI). */
  group: string;
  outcomes: Outcome[];
  identity: string;
  proxy: string;
  node: string;
  /** HTTP status; undefined = any, 0 = no response. */
  status: number | undefined;
  /** Minimum latency in ms; 0 = no bound. */
  minLatency: number;
}

export const DEFAULT_REQUEST_FILTERS: RequestFilters = {
  range: presetSelection(DEFAULT_REQUEST_RANGE),
  site: '',
  group: '',
  outcomes: [],
  identity: '',
  proxy: '',
  node: '',
  status: undefined,
  minLatency: 0,
};

/** URL keys: range from to site group outcomes identity proxy node status latency. */
export function parseRequestFilters(search: SearchRecord): RequestFilters {
  const site = readString(search, 'site', 64);
  return {
    range: readTimeRange(search, DEFAULT_REQUEST_RANGE),
    site,
    // An endpoint group filter only makes sense within a site.
    group: site ? readString(search, 'group', 64) : '',
    outcomes: readList(search, 'outcomes').filter(isOutcome),
    identity: readString(search, 'identity', 64),
    proxy: readString(search, 'proxy', 64),
    node: readString(search, 'node'),
    status: readInt(search, 'status', 0, MAX_HTTP_STATUS),
    minLatency: readInt(search, 'latency', 0, MAX_MIN_LATENCY_MS) ?? 0,
  };
}

/** Search params of the filters; defaults are omitted. */
export function requestFiltersToSearch(filters: RequestFilters): SearchRecord {
  return compactSearch({
    ...timeRangeToSearch(filters.range, DEFAULT_REQUEST_RANGE),
    site: filters.site,
    group: filters.site ? filters.group : '',
    outcomes: filters.outcomes,
    identity: filters.identity,
    proxy: filters.proxy,
    node: filters.node,
    status: filters.status,
    latency: filters.minLatency > 0 ? filters.minLatency : undefined,
  });
}

/** Number of filters that differ from the defaults (time range excluded). */
export function countActiveRequestFilters(filters: RequestFilters): number {
  return [
    filters.site,
    filters.group,
    filters.outcomes.length > 0,
    filters.identity,
    filters.proxy,
    filters.node,
    filters.status !== undefined,
    filters.minLatency > 0,
  ].filter(Boolean).length;
}

/** Filters reset to defaults while keeping the time range. */
export function resetRequestFilters(filters: RequestFilters): RequestFilters {
  return { ...DEFAULT_REQUEST_FILTERS, range: filters.range };
}

/** Request fields (without paging) for QueryRequestEvents. */
export function toQueryRequest(
  filters: RequestFilters,
  namespace: string,
  anchorMs: number,
): Pick<
  QueryRequestEventsRequest,
  | 'namespace'
  | 'site'
  | 'endpointGroupId'
  | 'outcomes'
  | 'identityId'
  | 'proxyId'
  | 'node'
  | 'httpStatus'
  | 'minLatencyMs'
  | 'timeRange'
> {
  return {
    namespace,
    site: filters.site,
    endpointGroupId: filters.site ? filters.group : '',
    outcomes: filters.outcomes,
    identityId: filters.identity,
    proxyId: filters.proxy,
    node: filters.node,
    httpStatus: filters.status,
    minLatencyMs: filters.minLatency,
    timeRange: toTimeRange(filters.range, anchorMs),
  };
}

/** Parses the HTTP status input ("" = any). */
export function parseStatusInput(input: string): number | undefined {
  return parseIntInput(input, 0, MAX_HTTP_STATUS);
}

/** Parses the minimum latency input in ms ("" = no bound). */
export function parseLatencyInput(input: string): number {
  return parseIntInput(input, 0, MAX_MIN_LATENCY_MS) ?? 0;
}
