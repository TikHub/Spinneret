import { lazy, Suspense } from 'react';

import { Skeleton } from '@/components/ui/skeleton';

import { type EChartProps } from './types';

const EChartImpl = lazy(() => import('./EChartImpl'));

export type { EChartProps, EChartsOption } from './types';

/**
 * Theme-aware ECharts wrapper (line, bar, pie, heatmap, scatter). ECharts is
 * loaded on first render in its own chunk; pass a memoized `option`.
 */
export function EChart(props: EChartProps) {
  return (
    <Suspense fallback={<Skeleton className="w-full" style={{ height: props.height ?? 240 }} />}>
      <EChartImpl {...props} />
    </Suspense>
  );
}
