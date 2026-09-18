/** Proxy lifecycle states accepted by ListProxiesRequest.states. */
export const PROXY_STATES = ['active', 'disabled', 'dead', 'banned', 'quarantined', 'retired'] as const;
export type ProxyState = (typeof PROXY_STATES)[number];

/** Proxy kinds accepted by ListProxiesRequest.kinds and the edit/import forms. */
export const PROXY_KINDS = ['datacenter', 'residential', 'mobile', 'tunnel'] as const;
export type ProxyKind = (typeof PROXY_KINDS)[number];

/** List limits of ListProxiesRequest (proto validation). */
export const FILTER_LIMITS = {
  providers: 64,
  regions: 64,
  tags: 32,
  providerLength: 128,
  regionLength: 64,
  tagLength: 64,
  search: 256,
} as const;

/** Filters of the proxies table, mirrored in the URL. */
export interface ProxyFilters {
  states: string[];
  kinds: string[];
  providers: string[];
  regions: string[];
  tags: string[];
  search: string;
}

export const EMPTY_PROXY_FILTERS: ProxyFilters = {
  states: [],
  kinds: [],
  providers: [],
  regions: [],
  tags: [],
  search: '',
};

/** URL search keys used by the proxies page. */
export const PROXY_SEARCH_KEYS = {
  states: 'states',
  kinds: 'kinds',
  providers: 'providers',
  regions: 'regions',
  tags: 'tags',
  search: 'q',
} as const;

/**
 * Text of a scalar URL search value. The router parses hand-typed values as
 * JSON, so "?tags=2024" arrives as the number 2024; numbers are read as text.
 */
function searchText(value: unknown): string | undefined {
  if (typeof value === 'string') return value;
  if (typeof value === 'number' && Number.isFinite(value)) return String(value);
  return undefined;
}

/**
 * Parses a list from a URL search value: a comma-separated string or an array
 * of strings. Values are trimmed, deduplicated and empty entries dropped.
 */
export function parseList(value: unknown, maxItems = Infinity, maxLength = Infinity): string[] {
  let raw: unknown[];
  const text = searchText(value);
  if (text !== undefined) raw = text.split(',');
  else if (Array.isArray(value)) raw = value;
  else return [];
  const out: string[] = [];
  for (const item of raw) {
    const itemText = searchText(item);
    if (itemText === undefined) continue;
    const trimmed = itemText.trim();
    if (trimmed === '' || trimmed.length > maxLength || out.includes(trimmed)) continue;
    out.push(trimmed);
    if (out.length >= maxItems) break;
  }
  return out;
}

function parseEnumList(value: unknown, allowed: readonly string[]): string[] {
  return parseList(value).filter((v) => allowed.includes(v));
}

/** Reads and validates the proxy filters from URL search params. */
export function parseProxyFilters(search: Readonly<Record<string, unknown>>): ProxyFilters {
  const rawSearch = searchText(search[PROXY_SEARCH_KEYS.search]);
  return {
    states: parseEnumList(search[PROXY_SEARCH_KEYS.states], PROXY_STATES),
    kinds: parseEnumList(search[PROXY_SEARCH_KEYS.kinds], PROXY_KINDS),
    providers: parseList(
      search[PROXY_SEARCH_KEYS.providers],
      FILTER_LIMITS.providers,
      FILTER_LIMITS.providerLength,
    ),
    regions: parseList(search[PROXY_SEARCH_KEYS.regions], FILTER_LIMITS.regions, FILTER_LIMITS.regionLength),
    tags: parseList(search[PROXY_SEARCH_KEYS.tags], FILTER_LIMITS.tags, FILTER_LIMITS.tagLength),
    search: rawSearch === undefined ? '' : rawSearch.slice(0, FILTER_LIMITS.search),
  };
}

/** Serializes a list for the URL: comma-joined unless a value contains a comma. */
function serializeList(values: readonly string[]): string | string[] | undefined {
  if (values.length === 0) return undefined;
  return values.some((v) => v.includes(',')) ? [...values] : values.join(',');
}

/** URL search params for the filters; empty filters become undefined (removed from the URL). */
export function proxyFiltersToSearch(
  filters: ProxyFilters,
): Record<(typeof PROXY_SEARCH_KEYS)[keyof typeof PROXY_SEARCH_KEYS], string | string[] | undefined> {
  return {
    [PROXY_SEARCH_KEYS.states]: serializeList(filters.states),
    [PROXY_SEARCH_KEYS.kinds]: serializeList(filters.kinds),
    [PROXY_SEARCH_KEYS.providers]: serializeList(filters.providers),
    [PROXY_SEARCH_KEYS.regions]: serializeList(filters.regions),
    [PROXY_SEARCH_KEYS.tags]: serializeList(filters.tags),
    [PROXY_SEARCH_KEYS.search]: filters.search.trim() === '' ? undefined : filters.search,
  };
}

/** Number of active filter groups (search counts as one). */
export function countActiveFilters(filters: ProxyFilters): number {
  return (
    [filters.states, filters.kinds, filters.providers, filters.regions, filters.tags].filter(
      (list) => list.length > 0,
    ).length + (filters.search.trim() === '' ? 0 : 1)
  );
}
