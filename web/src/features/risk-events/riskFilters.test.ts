import { timestampMs } from '@bufbuild/protobuf/wkt';
import { describe, expect, it } from 'vitest';

import { presetSelection } from '@/features/requests/shared/timeRange';
import { DAY_MS } from '@/lib/time';

import {
  countActiveRiskFilters,
  DEFAULT_RISK_FILTERS,
  parseRiskFilters,
  riskFiltersToSearch,
  toRiskEventsRequest,
  type RiskFilters,
} from './riskFilters';

const FULL: RiskFilters = {
  range: presetSelection('7d'),
  site: 'shop',
  group: 'eg_9',
  outcome: 'captcha',
  identity: 'idn_1',
  proxy: 'prx_1',
  node: 'node-a',
};

describe('risk filters <-> URL', () => {
  it('defaults to the last 24 hours without params', () => {
    expect(parseRiskFilters({})).toEqual(DEFAULT_RISK_FILTERS);
    expect(DEFAULT_RISK_FILTERS.range).toEqual(presetSelection('24h'));
    expect(riskFiltersToSearch(DEFAULT_RISK_FILTERS)).toEqual({});
  });

  it('round-trips every filter', () => {
    const search = riskFiltersToSearch(FULL);
    expect(search).toEqual({
      range: '7d',
      site: 'shop',
      group: 'eg_9',
      outcome: 'captcha',
      identity: 'idn_1',
      proxy: 'prx_1',
      node: 'node-a',
    });
    expect(parseRiskFilters(search)).toEqual(FULL);
    expect(countActiveRiskFilters(FULL)).toBe(6);
  });

  it('rejects success and unknown outcomes and groups without a site', () => {
    expect(parseRiskFilters({ outcome: 'success' }).outcome).toBe('');
    expect(parseRiskFilters({ outcome: 'bogus' }).outcome).toBe('');
    expect(parseRiskFilters({ group: 'eg_9' }).group).toBe('');
  });

  it('builds an anchored request', () => {
    const anchor = Date.UTC(2026, 8, 17);
    const req = toRiskEventsRequest({ ...DEFAULT_RISK_FILTERS, outcome: 'banned' }, 'default', anchor);
    expect(req).toMatchObject({ namespace: 'default', outcome: 'banned', site: '', endpointGroupId: '' });
    expect(req.timeRange?.start && timestampMs(req.timeRange.start)).toBe(anchor - DAY_MS);
    expect(req.timeRange?.end && timestampMs(req.timeRange.end)).toBe(anchor);
  });
});
