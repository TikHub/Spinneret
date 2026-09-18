import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';

import { useTheme } from '@/app/theme/ThemeProvider';
import { EChart, type EChartsOption } from '@/components/charts/EChart';
import { formatNumber, formatPercent } from '@/lib/format';

import { outcomeChartColor, OTHER_OUTCOMES_COLOR, type OutcomeSlice } from '../shared/outcomes';

export interface OutcomeDonutProps {
  slices: readonly OutcomeSlice[];
  height?: number;
}

/** Donut of the outcome distribution with counts and shares in the legend and tooltip. */
export function OutcomeDonut({ slices, height = 200 }: OutcomeDonutProps) {
  const { t, i18n } = useTranslation('requests');
  const lng = i18n.language;
  const { resolvedTheme } = useTheme();

  const option = useMemo<EChartsOption>(() => {
    const data = slices.map((slice) => ({
      name: slice.outcome
        ? t(`shared.outcomes.${slice.outcome}`, { defaultValue: slice.outcome })
        : t('summary.otherOutcomes'),
      value: slice.count,
      ratio: slice.ratio,
      itemStyle: {
        color: slice.outcome
          ? outcomeChartColor(slice.outcome, resolvedTheme)
          : OTHER_OUTCOMES_COLOR[resolvedTheme],
      },
    }));
    const byName = new Map(data.map((d) => [d.name, d]));
    const describe = (name: string) => {
      const item = byName.get(name);
      return item ? `${formatNumber(item.value, undefined, lng)} (${formatPercent(item.ratio, 1, lng)})` : '';
    };
    return {
      animation: false,
      tooltip: {
        trigger: 'item',
        formatter: (params: unknown) => {
          const name = (params as { name?: string }).name ?? '';
          return `${name}: ${describe(name)}`;
        },
      },
      legend: {
        orient: 'vertical',
        right: 0,
        top: 'middle',
        itemWidth: 10,
        itemHeight: 10,
        icon: 'circle',
        formatter: (name: string) => `${name}  ${describe(name)}`,
      },
      series: [
        {
          type: 'pie',
          radius: ['52%', '78%'],
          center: ['28%', '50%'],
          avoidLabelOverlap: true,
          label: { show: false },
          labelLine: { show: false },
          itemStyle: {
            borderWidth: 2,
            borderColor: resolvedTheme === 'dark' ? '#0f172a' : '#ffffff',
            borderRadius: 3,
          },
          data,
        },
      ],
    };
  }, [slices, t, lng, resolvedTheme]);

  return <EChart option={option} height={height} aria-label={t('summary.outcomes')} />;
}
