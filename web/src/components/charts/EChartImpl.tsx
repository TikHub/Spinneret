import { BarChart, HeatmapChart, LineChart, PieChart, ScatterChart } from 'echarts/charts';
import {
  DataZoomComponent,
  DatasetComponent,
  GridComponent,
  LegendComponent,
  MarkAreaComponent,
  MarkLineComponent,
  TitleComponent,
  TooltipComponent,
  VisualMapComponent,
} from 'echarts/components';
import * as echarts from 'echarts/core';
import { CanvasRenderer } from 'echarts/renderers';
import { useEffect, useLayoutEffect, useRef } from 'react';

import { useTheme } from '@/app/theme/ThemeProvider';
import { cn } from '@/lib/utils';

import { CHART_COLORS, CHART_TEXT } from './palette';
import { type EChartProps } from './types';

echarts.use([
  LineChart,
  BarChart,
  PieChart,
  HeatmapChart,
  ScatterChart,
  GridComponent,
  TooltipComponent,
  LegendComponent,
  DatasetComponent,
  TitleComponent,
  VisualMapComponent,
  DataZoomComponent,
  MarkLineComponent,
  MarkAreaComponent,
  CanvasRenderer,
]);

function registerThemes(): void {
  for (const mode of ['light', 'dark'] as const) {
    const c = CHART_TEXT[mode];
    echarts.registerTheme(`spinneret-${mode}`, {
      color: [...CHART_COLORS[mode]],
      backgroundColor: 'transparent',
      textStyle: { color: c.text, fontFamily: 'inherit' },
      legend: { textStyle: { color: c.text } },
      tooltip: {
        backgroundColor: c.tooltipBg,
        borderColor: c.tooltipBorder,
        textStyle: { color: mode === 'dark' ? '#e2e8f0' : '#0f172a' },
      },
      categoryAxis: {
        axisLine: { lineStyle: { color: c.axis } },
        axisTick: { lineStyle: { color: c.axis } },
        axisLabel: { color: c.text },
        splitLine: { lineStyle: { color: c.split } },
      },
      valueAxis: {
        axisLine: { lineStyle: { color: c.axis } },
        axisLabel: { color: c.text },
        splitLine: { lineStyle: { color: c.split } },
      },
      timeAxis: {
        axisLine: { lineStyle: { color: c.axis } },
        axisLabel: { color: c.text },
        splitLine: { lineStyle: { color: c.split } },
      },
    });
  }
}

registerThemes();

/** ECharts implementation (loaded lazily): init/dispose per theme, resize observer, option updates. */
export default function EChartImpl({
  option,
  height = 240,
  className,
  loading = false,
  setOptionOpts,
  onEvents,
  'aria-label': ariaLabel,
}: EChartProps) {
  const { resolvedTheme } = useTheme();
  const containerRef = useRef<HTMLDivElement>(null);
  const chartRef = useRef<echarts.ECharts | undefined>(undefined);
  const latest = useRef({ option, setOptionOpts, onEvents });

  useLayoutEffect(() => {
    latest.current = { option, setOptionOpts, onEvents };
  });

  // (Re)create the instance when the theme changes.
  useEffect(() => {
    const el = containerRef.current;
    if (!el) return undefined;
    const chart = echarts.init(el, `spinneret-${resolvedTheme}`, { renderer: 'canvas' });
    chartRef.current = chart;
    chart.setOption(latest.current.option, latest.current.setOptionOpts ?? { notMerge: true });
    const observer = new ResizeObserver(() => chart.resize());
    observer.observe(el);
    return () => {
      observer.disconnect();
      chart.dispose();
      chartRef.current = undefined;
    };
  }, [resolvedTheme]);

  useEffect(() => {
    chartRef.current?.setOption(option, setOptionOpts ?? { notMerge: true });
  }, [option, setOptionOpts]);

  useEffect(() => {
    const chart = chartRef.current;
    if (!chart || !onEvents) return undefined;
    const entries = Object.entries(onEvents);
    for (const [name, handler] of entries) chart.on(name, handler);
    return () => {
      // On a theme change the instance is disposed (and recreated) before this cleanup runs.
      if (chart.isDisposed()) return;
      for (const [name, handler] of entries) chart.off(name, handler);
    };
  }, [onEvents, resolvedTheme]);

  useEffect(() => {
    const chart = chartRef.current;
    if (!chart) return;
    if (loading) chart.showLoading({ text: '', maskColor: 'transparent' });
    else chart.hideLoading();
  }, [loading, resolvedTheme]);

  return (
    <div
      ref={containerRef}
      role="img"
      aria-label={ariaLabel}
      className={cn('w-full', className)}
      style={{ height }}
    />
  );
}
