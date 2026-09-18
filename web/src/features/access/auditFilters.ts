import { create } from '@bufbuild/protobuf';

import { TimeRangeSchema, type TimeRange } from '@/gen/spinneret/v1/common_pb';
import { DAY_MS, HOUR_MS, toTimestamp } from '@/lib/time';

/** Result values of an audit log entry. */
export const AUDIT_RESULTS = ['ok', 'denied', 'error'] as const;
export type AuditResult = (typeof AUDIT_RESULTS)[number];

/** Relative time range presets; "custom" uses explicit bounds. */
export const AUDIT_RANGE_PRESETS = ['1h', '24h', '7d', '30d'] as const;
export type AuditRangePreset = (typeof AUDIT_RANGE_PRESETS)[number];
export type AuditRange = AuditRangePreset | 'custom';

const PRESET_MS: Record<AuditRangePreset, number> = {
  '1h': HOUR_MS,
  '24h': DAY_MS,
  '7d': 7 * DAY_MS,
  '30d': 30 * DAY_MS,
};

/** Audit log filters as kept in the URL. */
export interface AuditFilters {
  /** Namespace name; empty = every namespace (and tenant-level entries). */
  namespace: string;
  actor: string;
  action: string;
  resourceKind: string;
  resourceId: string;
  /** Empty = any result. */
  result: AuditResult | '';
  range: AuditRange;
  /** Local "yyyy-MM-ddTHH:mm" lower bound for the custom range; empty = open. */
  from: string;
  /** Local "yyyy-MM-ddTHH:mm" upper bound for the custom range; empty = open. */
  to: string;
}

export const DEFAULT_AUDIT_FILTERS: AuditFilters = {
  namespace: '',
  actor: '',
  action: '',
  resourceKind: '',
  resourceId: '',
  result: '',
  range: '24h',
  from: '',
  to: '',
};

/** Max lengths of the ListAuditLogsRequest filters. */
const MAX_LENGTH: Record<'namespace' | 'actor' | 'action' | 'resourceKind' | 'resourceId', number> = {
  namespace: 64,
  actor: 128,
  action: 128,
  resourceKind: 64,
  resourceId: 128,
};

const TEXT_KEYS = ['namespace', 'actor', 'action', 'resourceKind', 'resourceId'] as const;

/** Coerces a search param value (the router may parse numbers and booleans) to a string. */
function asString(value: unknown): string {
  if (typeof value === 'string') return value;
  if (typeof value === 'number' || typeof value === 'boolean') return String(value);
  return '';
}

function isResult(value: string): value is AuditResult {
  return (AUDIT_RESULTS as readonly string[]).includes(value);
}

function isRange(value: string): value is AuditRange {
  return value === 'custom' || (AUDIT_RANGE_PRESETS as readonly string[]).includes(value);
}

/** Reads filters from URL search params, ignoring invalid values. */
export function parseAuditSearch(search: Readonly<Record<string, unknown>>): AuditFilters {
  const filters: AuditFilters = { ...DEFAULT_AUDIT_FILTERS };
  for (const key of TEXT_KEYS) {
    filters[key] = asString(search[key]).slice(0, MAX_LENGTH[key]);
  }
  const result = asString(search.result);
  if (isResult(result)) filters.result = result;
  const range = asString(search.range);
  if (isRange(range)) filters.range = range;
  if (filters.range === 'custom') {
    filters.from = parseLocalDateTime(asString(search.from)) ? asString(search.from) : '';
    filters.to = parseLocalDateTime(asString(search.to)) ? asString(search.to) : '';
  }
  return filters;
}

/** Search params for filters; default values are omitted to keep URLs short. */
export function toAuditSearch(filters: AuditFilters): Record<string, string | undefined> {
  const search: Record<string, string | undefined> = {};
  for (const key of TEXT_KEYS) {
    const value = filters[key].trim();
    search[key] = value === '' ? undefined : value;
  }
  search.result = filters.result === '' ? undefined : filters.result;
  search.range = filters.range === DEFAULT_AUDIT_FILTERS.range ? undefined : filters.range;
  search.from = filters.range === 'custom' && filters.from !== '' ? filters.from : undefined;
  search.to = filters.range === 'custom' && filters.to !== '' ? filters.to : undefined;
  return search;
}

/** Number of filters that differ from the defaults. */
export function activeAuditFilterCount(filters: AuditFilters): number {
  let count = TEXT_KEYS.filter((key) => filters[key].trim() !== '').length;
  if (filters.result !== '') count += 1;
  if (filters.range !== DEFAULT_AUDIT_FILTERS.range) count += 1;
  return count;
}

const LOCAL_DATE_TIME = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::(\d{2}))?$/;

/** Parses a local "yyyy-MM-ddTHH:mm[:ss]" value (datetime-local input); undefined when invalid. */
export function parseLocalDateTime(value: string): Date | undefined {
  const match = LOCAL_DATE_TIME.exec(value);
  if (!match) return undefined;
  const [, y, mo, d, h, mi, s] = match;
  const date = new Date(Number(y), Number(mo) - 1, Number(d), Number(h), Number(mi), Number(s ?? '0'));
  return Number.isNaN(date.getTime()) || date.getMonth() !== Number(mo) - 1 ? undefined : date;
}

/** Resolved time bounds in epoch ms; 'invalid' when the custom end is not after its start. */
export function resolveAuditTimeRange(
  filters: AuditFilters,
  now: number,
): { startMs?: number; endMs?: number } | 'invalid' {
  if (filters.range !== 'custom') {
    return { startMs: now - PRESET_MS[filters.range], endMs: now };
  }
  const start = parseLocalDateTime(filters.from)?.getTime();
  const end = parseLocalDateTime(filters.to)?.getTime();
  if (start !== undefined && end !== undefined && end <= start) return 'invalid';
  return { startMs: start, endMs: end };
}

/**
 * ListAuditLogsRequest fields for the filters (without paging).
 *
 * The server fills an unset start with "now - 24h" and an unset end with its
 * own clock (plus skew allowance), so:
 * - presets send only the start: the newest entries are never hidden by a
 *   browser clock that lags behind the server (live tailing);
 * - a custom range without a start sends the Unix epoch, otherwise an end
 *   older than 24 hours would be rejected as "end before start".
 */
export function toListAuditLogsInit(
  filters: AuditFilters,
  now: number,
): {
  namespace: string;
  actor: string;
  action: string;
  resourceKind: string;
  resourceId: string;
  result: string;
  timeRange?: TimeRange;
} {
  const init = {
    namespace: filters.namespace.trim(),
    actor: filters.actor.trim(),
    action: filters.action.trim(),
    resourceKind: filters.resourceKind.trim(),
    resourceId: filters.resourceId.trim(),
    result: filters.result,
  };
  const range = resolveAuditTimeRange(filters, now);
  // An invalid custom range is never queried (the page disables the query).
  if (range === 'invalid') return init;
  const end = filters.range === 'custom' && range.endMs !== undefined ? toTimestamp(range.endMs) : undefined;
  return {
    ...init,
    timeRange: create(TimeRangeSchema, { start: toTimestamp(range.startMs ?? 0), end }),
  };
}
