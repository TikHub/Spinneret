import { type MessageInitShape } from '@bufbuild/protobuf';
import { type SortingState } from '@tanstack/react-table';

import { type IdentityFilterSchema } from '@/gen/spinneret/v1/identity_admin_pb';
import type { PageSearch } from '@/router';

/** Lifecycle states accepted by IdentityFilter.states. */
export const IDENTITY_STATES = [
  'pending',
  'active',
  'expired',
  'banned',
  'quarantined',
  'disabled',
  'retired',
] as const;
export type IdentityState = (typeof IDENTITY_STATES)[number];

/** Sort keys accepted by ListIdentitiesRequest.order_by ("" = created_at). */
export const IDENTITY_ORDER_BY = [
  'created_at',
  'updated_at',
  'state_changed_at',
  'last_used_at',
  'score',
] as const;
export type IdentityOrderBy = (typeof IDENTITY_ORDER_BY)[number];

/** URL search keys of the identities page (named after the proto fields). */
export const IDENTITY_SEARCH_KEYS = [
  'site',
  'type',
  'states',
  'tags',
  'account_ref',
  'region',
  'search',
  'min_score',
  'max_score',
  'include_retired',
  'order_by',
  'descending',
] as const;

/** Normalized identities list parameters (filter + sort), derived from the URL. */
export interface IdentityListParams {
  site: string;
  type: string;
  states: IdentityState[];
  tags: string[];
  accountRef: string;
  region: string;
  search: string;
  minScore?: number;
  maxScore?: number;
  includeRetired: boolean;
  orderBy: IdentityOrderBy | '';
  descending: boolean;
}

export const EMPTY_IDENTITY_PARAMS: IdentityListParams = {
  site: '',
  type: '',
  states: [],
  tags: [],
  accountRef: '',
  region: '',
  search: '',
  minScore: undefined,
  maxScore: undefined,
  includeRetired: false,
  orderBy: '',
  descending: false,
};

const MAX_TEXT = { site: 64, type: 64, accountRef: 256, region: 64, search: 256 } as const;
const MAX_TAGS = 32;
const MAX_TAG_LENGTH = 64;

function readString(value: unknown, maxLength: number): string {
  if (typeof value === 'number' && Number.isFinite(value)) return String(value).slice(0, maxLength);
  if (typeof value !== 'string') return '';
  return value.trim().slice(0, maxLength);
}

/** Reads a list from an array or a comma-separated string; trims, drops empties and duplicates. */
export function readList(value: unknown): string[] {
  let raw: unknown[] = [];
  if (Array.isArray(value)) raw = value;
  else if (typeof value === 'string') raw = value.split(',');
  else if (typeof value === 'number') raw = [String(value)];
  const out: string[] = [];
  for (const item of raw) {
    const s = typeof item === 'number' ? String(item) : typeof item === 'string' ? item.trim() : '';
    if (s && !out.includes(s)) out.push(s);
  }
  return out;
}

function readBool(value: unknown): boolean {
  return value === true || value === 'true' || value === 1 || value === '1';
}

function readScore(value: unknown): number | undefined {
  if (value === undefined || value === null || value === '') return undefined;
  const n = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : Number.NaN;
  if (!Number.isFinite(n)) return undefined;
  return Math.min(100, Math.max(0, n));
}

function isState(value: string): value is IdentityState {
  return (IDENTITY_STATES as readonly string[]).includes(value);
}

function isOrderBy(value: string): value is IdentityOrderBy {
  return (IDENTITY_ORDER_BY as readonly string[]).includes(value);
}

/** Parses and validates the identities page search params. Invalid values are dropped. */
export function parseIdentitySearch(search: PageSearch): IdentityListParams {
  const states = readList(search.states).filter(isState);
  const tags = readList(search.tags)
    .filter((tag) => tag.length <= MAX_TAG_LENGTH)
    .slice(0, MAX_TAGS);
  let minScore = readScore(search.min_score);
  let maxScore = readScore(search.max_score);
  if (minScore !== undefined && maxScore !== undefined && minScore > maxScore) {
    [minScore, maxScore] = [maxScore, minScore];
  }
  const orderBy = readString(search.order_by, 32);
  return {
    site: readString(search.site, MAX_TEXT.site),
    type: readString(search.type, MAX_TEXT.type),
    states,
    tags,
    accountRef: readString(search.account_ref, MAX_TEXT.accountRef),
    region: readString(search.region, MAX_TEXT.region),
    search: readString(search.search, MAX_TEXT.search),
    minScore,
    maxScore,
    includeRetired: readBool(search.include_retired),
    orderBy: isOrderBy(orderBy) ? orderBy : '',
    descending: readBool(search.descending),
  };
}

/** Serializes list parameters to search params, omitting defaults. */
export function identityParamsToSearch(params: IdentityListParams): PageSearch {
  const out: PageSearch = {};
  if (params.site) out.site = params.site;
  if (params.type) out.type = params.type;
  if (params.states.length > 0) out.states = params.states.join(',');
  if (params.tags.length > 0) out.tags = params.tags.join(',');
  if (params.accountRef) out.account_ref = params.accountRef;
  if (params.region) out.region = params.region;
  if (params.search) out.search = params.search;
  if (params.minScore !== undefined) out.min_score = params.minScore;
  if (params.maxScore !== undefined) out.max_score = params.maxScore;
  if (params.includeRetired) out.include_retired = true;
  if (params.orderBy) out.order_by = params.orderBy;
  if (params.descending) out.descending = true;
  return out;
}

/** Replaces the identities keys of `prev` with `params`, keeping unrelated keys. */
export function mergeIdentitySearch(prev: PageSearch, params: IdentityListParams): PageSearch {
  const rest: PageSearch = {};
  for (const [key, value] of Object.entries(prev)) {
    if (!(IDENTITY_SEARCH_KEYS as readonly string[]).includes(key)) rest[key] = value;
  }
  return { ...rest, ...identityParamsToSearch(params) };
}

/** Builds the IdentityFilter of ListIdentities / BulkOperateIdentities. */
export function toIdentityFilter(params: IdentityListParams): MessageInitShape<typeof IdentityFilterSchema> {
  return {
    site: params.site,
    type: params.type,
    states: [...params.states],
    tags: [...params.tags],
    accountRef: params.accountRef,
    region: params.region,
    search: params.search,
    minScore: params.minScore,
    maxScore: params.maxScore,
    includeRetired: params.includeRetired,
  };
}

/** Number of active filter conditions (sorting excluded). */
export function countActiveFilters(params: IdentityListParams): number {
  let count = 0;
  if (params.site) count += 1;
  if (params.type) count += 1;
  if (params.states.length > 0) count += 1;
  if (params.tags.length > 0) count += 1;
  if (params.accountRef) count += 1;
  if (params.region) count += 1;
  if (params.search) count += 1;
  if (params.minScore !== undefined || params.maxScore !== undefined) count += 1;
  if (params.includeRetired) count += 1;
  return count;
}

/** Filter parameters only (sorting reset), used by "reset filters". */
export function resetFilters(params: IdentityListParams): IdentityListParams {
  return { ...EMPTY_IDENTITY_PARAMS, orderBy: params.orderBy, descending: params.descending };
}

/** Table column ids that map to server sort keys. */
const SORT_COLUMNS: Record<string, IdentityOrderBy> = {
  globalScore: 'score',
  lastUsedAt: 'last_used_at',
  stateChangedAt: 'state_changed_at',
};

/** Table sorting state for the current order_by (created_at/updated_at have no column). */
export function sortingFromParams(params: IdentityListParams): SortingState {
  const column = Object.entries(SORT_COLUMNS).find(([, orderBy]) => orderBy === params.orderBy)?.[0];
  return column ? [{ id: column, desc: params.descending }] : [];
}

/** order_by and descending for a table sorting state. */
export function sortParamsFromSorting(
  sorting: SortingState,
): Pick<IdentityListParams, 'orderBy' | 'descending'> {
  const first = sorting[0];
  const orderBy = first ? SORT_COLUMNS[first.id] : undefined;
  if (!first || !orderBy) return { orderBy: '', descending: false };
  return { orderBy, descending: first.desc };
}
