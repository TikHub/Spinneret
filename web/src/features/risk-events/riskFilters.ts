import { type ListRiskEventsRequest } from '@/gen/spinneret/v1/dashboard_pb';

import { RISK_EVENT_OUTCOMES } from '@/features/requests/shared/outcomes';
import {
  compactSearch,
  readEnum,
  readString,
  type SearchRecord,
} from '@/features/requests/shared/searchParams';
import {
  presetSelection,
  readTimeRange,
  timeRangeToSearch,
  toTimeRange,
  type TimeRangePreset,
  type TimeRangeSelection,
} from '@/features/requests/shared/timeRange';

/** Default window of the risk event list (same as the server default). */
export const DEFAULT_RISK_RANGE: TimeRangePreset = '24h';

/** Filters of the risk event list, mirrored in the URL. */
export interface RiskFilters {
  range: TimeRangeSelection;
  /** Site name. */
  site: string;
  /** Endpoint group ID (requires a site in the UI). */
  group: string;
  /** Non-success outcome; empty = all. */
  outcome: string;
  identity: string;
  proxy: string;
  node: string;
}

export const DEFAULT_RISK_FILTERS: RiskFilters = {
  range: presetSelection(DEFAULT_RISK_RANGE),
  site: '',
  group: '',
  outcome: '',
  identity: '',
  proxy: '',
  node: '',
};

/** URL keys: range from to site group outcome identity proxy node. */
export function parseRiskFilters(search: SearchRecord): RiskFilters {
  const site = readString(search, 'site', 64);
  return {
    range: readTimeRange(search, DEFAULT_RISK_RANGE),
    site,
    group: site ? readString(search, 'group', 64) : '',
    outcome: readEnum(search, 'outcome', ['', ...RISK_EVENT_OUTCOMES], ''),
    identity: readString(search, 'identity', 64),
    proxy: readString(search, 'proxy', 64),
    node: readString(search, 'node'),
  };
}

/** Search params of the filters; defaults are omitted. */
export function riskFiltersToSearch(filters: RiskFilters): SearchRecord {
  return compactSearch({
    ...timeRangeToSearch(filters.range, DEFAULT_RISK_RANGE),
    site: filters.site,
    group: filters.site ? filters.group : '',
    outcome: filters.outcome,
    identity: filters.identity,
    proxy: filters.proxy,
    node: filters.node,
  });
}

/** Number of filters that differ from the defaults (time range excluded). */
export function countActiveRiskFilters(filters: RiskFilters): number {
  return [filters.site, filters.group, filters.outcome, filters.identity, filters.proxy, filters.node].filter(
    Boolean,
  ).length;
}

/** Request fields (without paging) for ListRiskEvents. */
export function toRiskEventsRequest(
  filters: RiskFilters,
  namespace: string,
  anchorMs: number,
): Pick<
  ListRiskEventsRequest,
  'namespace' | 'site' | 'endpointGroupId' | 'outcome' | 'identityId' | 'proxyId' | 'node' | 'timeRange'
> {
  return {
    namespace,
    site: filters.site,
    endpointGroupId: filters.site ? filters.group : '',
    outcome: filters.outcome,
    identityId: filters.identity,
    proxyId: filters.proxy,
    node: filters.node,
    timeRange: toTimeRange(filters.range, anchorMs),
  };
}
