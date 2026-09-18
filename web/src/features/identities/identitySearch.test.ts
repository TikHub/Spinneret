import { describe, expect, it } from 'vitest';

import {
  countActiveFilters,
  EMPTY_IDENTITY_PARAMS,
  identityParamsToSearch,
  mergeIdentitySearch,
  parseIdentitySearch,
  readList,
  resetFilters,
  sortingFromParams,
  sortParamsFromSorting,
  toIdentityFilter,
} from './identitySearch';

describe('parseIdentitySearch', () => {
  it('returns defaults for an empty search', () => {
    expect(parseIdentitySearch({})).toEqual(EMPTY_IDENTITY_PARAMS);
  });

  it('parses every key and drops invalid values', () => {
    const params = parseIdentitySearch({
      site: ' shop ',
      type: 'web_cookie',
      states: 'active,banned,bogus,active',
      tags: ['vip', ' us ', ''],
      account_ref: 'user-1',
      region: 'US',
      search: 12345,
      min_score: '20',
      max_score: 150,
      include_retired: true,
      order_by: 'score',
      descending: 'true',
    });
    expect(params).toEqual({
      site: 'shop',
      type: 'web_cookie',
      states: ['active', 'banned'],
      tags: ['vip', 'us'],
      accountRef: 'user-1',
      region: 'US',
      search: '12345',
      minScore: 20,
      maxScore: 100,
      includeRetired: true,
      orderBy: 'score',
      descending: true,
    });
  });

  it('swaps an inverted score range and ignores an unknown sort key', () => {
    const params = parseIdentitySearch({ min_score: 80, max_score: 10, order_by: 'name' });
    expect(params.minScore).toBe(10);
    expect(params.maxScore).toBe(80);
    expect(params.orderBy).toBe('');
  });

  it('ignores non-numeric scores', () => {
    expect(parseIdentitySearch({ min_score: 'abc' }).minScore).toBeUndefined();
  });
});

describe('identityParamsToSearch', () => {
  it('omits defaults and round-trips', () => {
    expect(identityParamsToSearch(EMPTY_IDENTITY_PARAMS)).toEqual({});
    const params = parseIdentitySearch({
      site: 'shop',
      states: 'pending,active',
      tags: 'a,b',
      min_score: 0,
      include_retired: true,
      order_by: 'last_used_at',
      descending: true,
    });
    const search = identityParamsToSearch(params);
    expect(search).toEqual({
      site: 'shop',
      states: 'pending,active',
      tags: 'a,b',
      min_score: 0,
      include_retired: true,
      order_by: 'last_used_at',
      descending: true,
    });
    expect(parseIdentitySearch(search)).toEqual(params);
  });

  it('merges into existing search params keeping unrelated keys', () => {
    const merged = mergeIdentitySearch(
      { site: 'old', region: 'EU', tab: 'x' },
      { ...EMPTY_IDENTITY_PARAMS, site: 'new' },
    );
    expect(merged).toEqual({ tab: 'x', site: 'new' });
  });
});

describe('toIdentityFilter and counting', () => {
  it('builds the proto filter', () => {
    const params = parseIdentitySearch({ site: 's', states: 'banned', max_score: 30, include_retired: 1 });
    expect(toIdentityFilter(params)).toEqual({
      site: 's',
      type: '',
      states: ['banned'],
      tags: [],
      accountRef: '',
      region: '',
      search: '',
      minScore: undefined,
      maxScore: 30,
      includeRetired: true,
    });
    expect(countActiveFilters(params)).toBe(4);
  });

  it('reset keeps sorting only', () => {
    const params = parseIdentitySearch({ site: 's', order_by: 'score', descending: true });
    expect(resetFilters(params)).toEqual({ ...EMPTY_IDENTITY_PARAMS, orderBy: 'score', descending: true });
  });
});

describe('sorting mapping', () => {
  it('maps order_by to table sorting and back', () => {
    const params = parseIdentitySearch({ order_by: 'state_changed_at', descending: true });
    const sorting = sortingFromParams(params);
    expect(sorting).toEqual([{ id: 'stateChangedAt', desc: true }]);
    expect(sortParamsFromSorting(sorting)).toEqual({ orderBy: 'state_changed_at', descending: true });
  });

  it('has no column sorting for created_at and clears on unknown columns', () => {
    expect(sortingFromParams(parseIdentitySearch({ order_by: 'created_at' }))).toEqual([]);
    expect(sortParamsFromSorting([{ id: 'site', desc: false }])).toEqual({ orderBy: '', descending: false });
    expect(sortParamsFromSorting([])).toEqual({ orderBy: '', descending: false });
  });
});

describe('readList', () => {
  it('accepts arrays, strings and numbers', () => {
    expect(readList(['a', 'a', 1])).toEqual(['a', '1']);
    expect(readList('x, y,,')).toEqual(['x', 'y']);
    expect(readList(7)).toEqual(['7']);
    expect(readList(undefined)).toEqual([]);
  });
});
