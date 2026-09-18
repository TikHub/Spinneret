import { useMemo } from 'react';

import { EChart, type EChartsOption } from '@/components/charts/EChart';
import { ErrorState } from '@/components/ErrorState';
import { type Series } from '@/gen/spinneret/v1/dashboard_pb';
import { toDate } from '@/lib/time';

export interface TrendSeries {
  name: string;
  series: readonly Series[] | undefined;
  color?: string;
}

export interface TrendChartProps {
  lines: readonly TrendSeries[];
  /** Formats axis and tooltip values. */
  formatValue: (value: number) => string;
  /** Fixed y-axis maximum (e.g. 1 for ratios). */
  yMax?: number;
  height?: number;
  loading?: boolean;
  /** Query error; shown instead of the chart while no line has data. */
  error?: unknown;
  onRetry?: () => void;
  ariaLabel: string;
}

function toPoints(series: readonly Series[] | undefined): Array<[number, number]> {
  const first = series?.[0];
  if (!first) return [];
  return first.points.flatMap((p) => {
    const date = toDate(p.ts);
    return date ? [[date.getTime(), p.value] as [number, number]] : [];
  });
}

/** Line chart over time for dashboard time series. */
export function TrendChart({
  lines,
  formatValue,
  yMax,
  height = 220,
  loading,
  error,
  onRetry,
  ariaLabel,
}: TrendChartProps) {
  const option = useMemo<EChartsOption>(
    () => ({
      animation: false,
      grid: { left: 8, right: 16, top: 28, bottom: 8, containLabel: true },
      legend: { top: 0, right: 0, itemWidth: 12, itemHeight: 8, show: lines.length > 1 },
      tooltip: {
        trigger: 'axis',
        valueFormatter: (value) => (typeof value === 'number' ? formatValue(value) : String(value ?? '')),
      },
      xAxis: { type: 'time', boundaryGap: [0, 0] },
      yAxis: {
        type: 'value',
        min: 0,
        max: yMax,
        splitNumber: 3,
        axisLabel: { formatter: (value: number) => formatValue(value) },
      },
      series: lines.map((line) => ({
        name: line.name,
        type: 'line',
        showSymbol: false,
        smooth: false,
        lineStyle: { width: 1.5 },
        areaStyle: lines.length === 1 ? { opacity: 0.08 } : undefined,
        itemStyle: line.color ? { color: line.color } : undefined,
        data: toPoints(line.series),
      })),
    }),
    [lines, formatValue, yMax],
  );

  const hasData = lines.some((line) => line.series !== undefined);
  if (error && !hasData) {
    return (
      <div className="flex items-center justify-center" style={{ height }}>
        <ErrorState error={error} onRetry={onRetry} compact />
      </div>
    );
  }
  return <EChart option={option} height={height} loading={loading} aria-label={ariaLabel} />;
}
