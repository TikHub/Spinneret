import { type EChartsOption } from '@/components/charts/EChart';
import { type ProviderStats } from '@/gen/spinneret/v1/proxy_admin_pb';
import { toNumber } from '@/lib/format';
import { DAY_MS, HOUR_MS } from '@/lib/time';

/** Time ranges offered by the provider statistics tab. */
export const PROVIDER_RANGES = ['1h', '24h', '7d'] as const;
export type ProviderRange = (typeof PROVIDER_RANGES)[number];
export const DEFAULT_PROVIDER_RANGE: ProviderRange = '24h';

export const PROVIDER_RANGE_MS: Readonly<Record<ProviderRange, number>> = {
  '1h': HOUR_MS,
  '24h': DAY_MS,
  '7d': 7 * DAY_MS,
};

/** Maximum providers drawn in the chart (the table lists all of them). */
export const CHART_PROVIDER_LIMIT = 20;

export function parseProviderRange(value: unknown): ProviderRange {
  return typeof value === 'string' && (PROVIDER_RANGES as readonly string[]).includes(value)
    ? (value as ProviderRange)
    : DEFAULT_PROVIDER_RANGE;
}

/** Bar chart input derived from provider statistics. */
export interface ProviderChartData {
  /** Provider labels (the empty provider becomes `noneLabel`). */
  categories: string[];
  /** Success ratio per provider, 0..1. */
  success: number[];
  /** Risk ratio per provider, 0..1. */
  risk: number[];
  /** Requests per provider (tooltip). */
  requests: number[];
}

function clampRatio(value: number): number {
  if (!Number.isFinite(value)) return 0;
  return Math.min(1, Math.max(0, value));
}

/**
 * Maps provider statistics to chart series. Providers without requests in the
 * range are skipped (their ratios carry no information); the rest keep the
 * server order (requests descending) and are capped at `limit`.
 */
export function toProviderChartData(
  stats: readonly ProviderStats[],
  noneLabel: string,
  limit: number = CHART_PROVIDER_LIMIT,
): ProviderChartData {
  const data: ProviderChartData = { categories: [], success: [], risk: [], requests: [] };
  for (const item of stats) {
    const requests = toNumber(item.requests);
    if (requests <= 0) continue;
    if (data.categories.length >= limit) break;
    data.categories.push(item.provider === '' ? noneLabel : item.provider);
    data.success.push(clampRatio(item.successRatio));
    data.risk.push(clampRatio(item.riskRatio));
    data.requests.push(requests);
  }
  return data;
}

export interface ProviderChartLabels {
  success: string;
  risk: string;
  requests: string;
}

export interface ProviderChartStyle {
  successColor: string;
  riskColor: string;
  formatPercent: (ratio: number) => string;
  formatCount: (value: number) => string;
}

/** ECharts option: grouped bars of success and risk ratio per provider on one 0..100% axis. */
export function buildProviderChartOption(
  data: ProviderChartData,
  labels: ProviderChartLabels,
  style: ProviderChartStyle,
): EChartsOption {
  return {
    animation: false,
    grid: { left: 8, right: 16, top: 32, bottom: 8, containLabel: true },
    legend: { top: 0, right: 0, itemWidth: 12, itemHeight: 8 },
    tooltip: {
      trigger: 'axis',
      axisPointer: { type: 'shadow' },
      formatter: (params: unknown) => {
        const items = Array.isArray(params) ? (params as Array<Record<string, unknown>>) : [];
        const index = typeof items[0]?.dataIndex === 'number' ? items[0].dataIndex : -1;
        if (index < 0) return '';
        const lines = items.map((item) => {
          const value = typeof item.value === 'number' ? style.formatPercent(item.value) : '';
          const marker = typeof item.marker === 'string' ? item.marker : '';
          return `${marker}${String(item.seriesName ?? '')}: <b>${value}</b>`;
        });
        const requests = style.formatCount(data.requests[index] ?? 0);
        return [escapeHtml(data.categories[index] ?? ''), ...lines, `${labels.requests}: ${requests}`].join(
          '<br/>',
        );
      },
    },
    xAxis: {
      type: 'category',
      data: data.categories,
      axisLabel: { interval: 0, hideOverlap: true, overflow: 'truncate', width: 96 },
    },
    yAxis: {
      type: 'value',
      min: 0,
      max: 1,
      splitNumber: 4,
      axisLabel: { formatter: (value: number) => style.formatPercent(value) },
    },
    series: [
      {
        name: labels.success,
        type: 'bar',
        data: data.success,
        barMaxWidth: 24,
        barGap: '10%',
        itemStyle: { color: style.successColor, borderRadius: [4, 4, 0, 0] },
      },
      {
        name: labels.risk,
        type: 'bar',
        data: data.risk,
        barMaxWidth: 24,
        itemStyle: { color: style.riskColor, borderRadius: [4, 4, 0, 0] },
      },
    ],
  };
}

/** Escapes provider names rendered inside the HTML tooltip. */
export function escapeHtml(value: string): string {
  return value
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll("'", '&#39;');
}
