import { timestampMs } from '@bufbuild/protobuf/wkt';
import { describe, expect, it } from 'vitest';

import { HOUR_MS } from '@/lib/time';

import {
  countActiveRequestFilters,
  DEFAULT_REQUEST_FILTERS,
  parseLatencyInput,
  parseRequestFilters,
  parseStatusInput,
  requestFiltersToSearch,
  resetRequestFilters,
  toQueryRequest,
  type RequestFilters,
} from './requestFilters';
import { presetSelection } from './shared/timeRange';

const FULL: RequestFilters = {
  range: presetSelection('6h'),
  site: 'shop',
  group: 'eg_1',
  outcomes: ['captcha', 'banned'],
  identity: 'idn_1',
  proxy: 'prx_1',
  node: 'crawler-hk-03',
  status: 403,
  minLatency: 1500,
};

describe('request filters <-> URL', () => {
  it('uses defaults for an empty search', () => {
    expect(parseRequestFilters({})).toEqual(DEFAULT_REQUEST_FILTERS);
    expect(requestFiltersToSearch(DEFAULT_REQUEST_FILTERS)).toEqual({});
  });

  it('round-trips every filter', () => {
    const search = requestFiltersToSearch(FULL);
    expect(search).toEqual({
      range: '6h',
      site: 'shop',
      group: 'eg_1',
      outcomes: 'captcha,banned',
      identity: 'idn_1',
      proxy: 'prx_1',
      node: 'crawler-hk-03',
      status: 403,
      latency: 1500,
    });
    expect(parseRequestFilters(search)).toEqual(FULL);
  });

  it('accepts router-parsed values (numbers, arrays) and drops invalid ones', () => {
    const filters = parseRequestFilters({
      site: 'shop',
      outcomes: ['captcha', 'nonsense', 'captcha'],
      status: '0',
      latency: -5,
      identity: '  idn_2 ',
    });
    expect(filters.outcomes).toEqual(['captcha']);
    expect(filters.status).toBe(0);
    expect(filters.minLatency).toBe(0);
    expect(filters.identity).toBe('idn_2');
    expect(parseRequestFilters({ status: 1200 }).status).toBeUndefined();
  });

  it('keeps status 0 (no response) in the URL', () => {
    expect(requestFiltersToSearch({ ...DEFAULT_REQUEST_FILTERS, status: 0 })).toEqual({ status: 0 });
  });

  it('ignores an endpoint group without a site', () => {
    expect(parseRequestFilters({ group: 'eg_1' }).group).toBe('');
    expect(requestFiltersToSearch({ ...DEFAULT_REQUEST_FILTERS, group: 'eg_1' })).toEqual({});
  });

  it('counts and resets active filters but keeps the range', () => {
    expect(countActiveRequestFilters(DEFAULT_REQUEST_FILTERS)).toBe(0);
    expect(countActiveRequestFilters(FULL)).toBe(8);
    expect(resetRequestFilters(FULL)).toEqual({ ...DEFAULT_REQUEST_FILTERS, range: presetSelection('6h') });
  });
});

describe('toQueryRequest', () => {
  it('maps filters to the request with an anchored time range', () => {
    const anchor = Date.UTC(2026, 8, 17, 12);
    const req = toQueryRequest(FULL, 'default', anchor);
    expect(req).toMatchObject({
      namespace: 'default',
      site: 'shop',
      endpointGroupId: 'eg_1',
      outcomes: ['captcha', 'banned'],
      identityId: 'idn_1',
      proxyId: 'prx_1',
      node: 'crawler-hk-03',
      httpStatus: 403,
      minLatencyMs: 1500,
    });
    expect(req.timeRange?.start && timestampMs(req.timeRange.start)).toBe(anchor - 6 * HOUR_MS);
    expect(req.timeRange?.end && timestampMs(req.timeRange.end)).toBe(anchor);
    expect(toQueryRequest(DEFAULT_REQUEST_FILTERS, 'default', anchor).httpStatus).toBeUndefined();
  });
});

describe('inputs', () => {
  it('parses status and latency inputs', () => {
    expect(parseStatusInput('')).toBeUndefined();
    expect(parseStatusInput('429')).toBe(429);
    expect(parseStatusInput('4xx')).toBeUndefined();
    expect(parseStatusInput('1000')).toBeUndefined();
    expect(parseLatencyInput('250')).toBe(250);
    expect(parseLatencyInput('-1')).toBe(0);
  });
});
