import type { EChartsOption, SetOptionOpts } from 'echarts';

export type { EChartsOption } from 'echarts';

export interface EChartProps {
  option: EChartsOption;
  /** CSS height; defaults to 240px. */
  height?: number | string;
  className?: string;
  /** Show the ECharts loading mask. */
  loading?: boolean;
  /** setOption behavior; defaults to { notMerge: true }. */
  setOptionOpts?: SetOptionOpts;
  /** Event handlers, e.g. { click: (params) => ... }. */
  onEvents?: Record<string, (params: unknown) => void>;
  'aria-label'?: string;
}
