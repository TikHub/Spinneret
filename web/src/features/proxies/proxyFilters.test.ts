import { describe, expect, it } from 'vitest';

import {
  countActiveFilters,
  EMPTY_PROXY_FILTERS,
  parseList,
  parseProxyFilters,
  proxyFiltersToSearch,
} from './proxyFilters';

describe('proxy filters and the URL', () => {
  it('parses comma lists and arrays, dropping unknown enum values', () => {
    const filters = parseProxyFilters({
      states: 'active,bogus,banned,active',
      kinds: ['tunnel', 'nope'],
      providers: 'acme, beta',
      regions: ['US', ''],
      tags: 'a',
      q: '203.0.113',
    });
    expect(filters).toEqual({
      states: ['active', 'banned'],
      kinds: ['tunnel'],
      providers: ['acme', 'beta'],
      regions: ['US'],
      tags: ['a'],
      search: '203.0.113',
    });
    expect(countActiveFilters(filters)).toBe(6);
  });

  it('round-trips through search params and removes empty filters', () => {
    const filters = { ...EMPTY_PROXY_FILTERS, states: ['dead'], providers: ['a,b', 'c'] };
    const search = proxyFiltersToSearch(filters);
    expect(search).toEqual({
      states: 'dead',
      kinds: undefined,
      providers: ['a,b', 'c'],
      regions: undefined,
      tags: undefined,
      q: undefined,
    });
    expect(parseProxyFilters(search)).toEqual(filters);
    expect(countActiveFilters(EMPTY_PROXY_FILTERS)).toBe(0);
    // A single provider containing a comma stays one value.
    const single = proxyFiltersToSearch({ ...EMPTY_PROXY_FILTERS, providers: ['acme, inc'] });
    expect(parseProxyFilters(single).providers).toEqual(['acme, inc']);
  });

  it('reads numeric values parsed from hand-typed URLs as text', () => {
    expect(parseProxyFilters({ tags: 2024, providers: [123, 'acme'], regions: 7, q: 42 })).toEqual({
      ...EMPTY_PROXY_FILTERS,
      tags: ['2024'],
      providers: ['123', 'acme'],
      regions: ['7'],
      search: '42',
    });
  });

  it('ignores malformed values and applies limits', () => {
    expect(parseProxyFilters({ states: 5, q: { x: 1 } })).toEqual(EMPTY_PROXY_FILTERS);
    expect(parseList('a,b,c', 2)).toEqual(['a', 'b']);
    expect(parseList(['long', 'x'], 10, 2)).toEqual(['x']);
  });
});
