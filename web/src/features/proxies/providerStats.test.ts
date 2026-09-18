import { create } from '@bufbuild/protobuf';
import { describe, expect, it } from 'vitest';

import { ProviderStatsSchema } from '@/gen/spinneret/v1/proxy_admin_pb';

import {
  buildProviderChartOption,
  escapeHtml,
  parseProviderRange,
  PROVIDER_RANGE_MS,
  toProviderChartData,
} from './providerStats';

const stats = [
  create(ProviderStatsSchema, { provider: 'acme', requests: 1200n, successRatio: 0.91, riskRatio: 0.04 }),
  create(ProviderStatsSchema, { provider: '', requests: 30n, successRatio: 1.2, riskRatio: Number.NaN }),
  create(ProviderStatsSchema, { provider: 'idle', requests: 0n, successRatio: 0, riskRatio: 0 }),
  create(ProviderStatsSchema, { provider: 'beta', requests: 10n, successRatio: 0.5, riskRatio: -0.1 }),
];

describe('toProviderChartData', () => {
  it('maps providers to categories and clamped ratios, skipping providers without requests', () => {
    expect(toProviderChartData(stats, '(none)')).toEqual({
      categories: ['acme', '(none)', 'beta'],
      success: [0.91, 1, 0.5],
      risk: [0.04, 0, 0],
      requests: [1200, 30, 10],
    });
  });

  it('caps the number of providers', () => {
    expect(toProviderChartData(stats, '-', 2).categories).toEqual(['acme', '-']);
  });

  it('returns empty series for no data', () => {
    expect(toProviderChartData([], '-')).toEqual({ categories: [], success: [], risk: [], requests: [] });
  });
});

describe('buildProviderChartOption', () => {
  it('builds two bar series on a single 0..1 axis', () => {
    const data = toProviderChartData(stats, '(none)');
    const option = buildProviderChartOption(
      data,
      { success: 'Success', risk: 'Risk', requests: 'Requests' },
      {
        successColor: '#111111',
        riskColor: '#222222',
        formatPercent: (v) => `${Math.round(v * 100)}%`,
        formatCount: String,
      },
    );
    const series = option.series as Array<{ name: string; type: string; data: number[] }>;
    expect(series.map((s) => [s.name, s.type])).toEqual([
      ['Success', 'bar'],
      ['Risk', 'bar'],
    ]);
    expect(series[0]?.data).toEqual(data.success);
    expect(series[1]?.data).toEqual(data.risk);
    expect(option.yAxis).toMatchObject({ min: 0, max: 1 });
    expect(option.xAxis).toMatchObject({ data: ['acme', '(none)', 'beta'] });
  });
});

describe('helpers', () => {
  it('parses the range with a 24h default', () => {
    expect(parseProviderRange('7d')).toBe('7d');
    expect(parseProviderRange('2w')).toBe('24h');
    expect(parseProviderRange(undefined)).toBe('24h');
    expect(PROVIDER_RANGE_MS['1h']).toBe(3_600_000);
  });

  it('escapes provider names for the tooltip', () => {
    expect(escapeHtml('<b>"x"&</b>')).toBe('&lt;b&gt;&quot;x&quot;&amp;&lt;/b&gt;');
  });
});
