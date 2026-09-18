import { ActivityIcon, ShieldAlertIcon, ShieldCheckIcon, TimerIcon } from 'lucide-react';
import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';

import { EmptyState } from '@/components/EmptyState';
import { StatCard } from '@/components/StatCard';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import { type RequestEventsSummary } from '@/gen/spinneret/v1/dashboard_pb';
import { formatLatency, formatNumber, formatPercent, toNumber } from '@/lib/format';

import { outcomeRatio, outcomeSlices, RISK_RATIO_OUTCOMES } from '../shared/outcomes';
import { OutcomeDonut } from './OutcomeDonut';

export interface RequestSummaryProps {
  summary: RequestEventsSummary | undefined;
  loading: boolean;
}

function LatencyFigure({
  label,
  value,
  loading,
}: {
  label: string;
  value: number | undefined;
  loading: boolean;
}) {
  return (
    <div className="grid gap-0.5">
      <span className="text-xs text-muted-foreground">{label}</span>
      {loading ? (
        <Skeleton className="h-6 w-16" />
      ) : (
        <span className="tabular text-lg leading-tight font-semibold">{formatLatency(value)}</span>
      )}
    </div>
  );
}

/** Summary cards of the matching events: totals, ratios, latency percentiles and outcome donut. */
export function RequestSummary({ summary, loading }: RequestSummaryProps) {
  const { t, i18n } = useTranslation('requests');
  const lng = i18n.language;
  const outcomes = summary?.outcomes;
  const slices = useMemo(() => outcomeSlices(outcomes ?? {}), [outcomes]);
  const total = toNumber(summary?.total);
  const successRatio = outcomes ? outcomeRatio(outcomes, ['success']) : undefined;
  const riskRatio = outcomes ? outcomeRatio(outcomes, RISK_RATIO_OUTCOMES) : undefined;
  const hasEvents = summary !== undefined && total > 0;
  const noEvents = !loading && summary !== undefined && total === 0;

  return (
    <section className="grid gap-3 lg:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_minmax(0,1.6fr)]">
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-1">
        <StatCard
          label={t('summary.total')}
          icon={ActivityIcon}
          loading={loading}
          value={summary ? formatNumber(total, undefined, lng) : '—'}
        />
        <div className="grid grid-cols-2 gap-3">
          <StatCard
            label={t('summary.successRatio')}
            icon={ShieldCheckIcon}
            loading={loading}
            tone={successRatio !== undefined && total > 0 && successRatio < 0.8 ? 'warning' : 'default'}
            value={total > 0 ? formatPercent(successRatio, 1, lng) : '—'}
          />
          <StatCard
            label={t('summary.riskRatio')}
            icon={ShieldAlertIcon}
            loading={loading}
            tone={riskRatio !== undefined && riskRatio >= 0.1 ? 'danger' : 'default'}
            value={total > 0 ? formatPercent(riskRatio, 1, lng) : '—'}
          />
        </div>
      </div>
      <Card className="gap-3 p-4">
        <div className="flex items-center justify-between">
          <span className="text-xs font-medium text-muted-foreground">{t('summary.latency')}</span>
          <TimerIcon className="size-4 text-muted-foreground" aria-hidden />
        </div>
        <div className="grid grid-cols-2 gap-4">
          <LatencyFigure
            label={t('summary.avg')}
            value={hasEvents ? summary?.latencyAvgMs : undefined}
            loading={loading}
          />
          <LatencyFigure
            label={t('summary.p50')}
            value={hasEvents ? summary?.latencyP50Ms : undefined}
            loading={loading}
          />
          <LatencyFigure
            label={t('summary.p95')}
            value={hasEvents ? summary?.latencyP95Ms : undefined}
            loading={loading}
          />
          <LatencyFigure
            label={t('summary.p99')}
            value={hasEvents ? summary?.latencyP99Ms : undefined}
            loading={loading}
          />
        </div>
      </Card>
      <Card>
        <CardHeader className="pb-0">
          <CardTitle className="text-xs font-medium text-muted-foreground">{t('summary.outcomes')}</CardTitle>
        </CardHeader>
        <CardContent>
          {loading ? (
            <Skeleton className="h-[180px] w-full" />
          ) : noEvents || slices.length === 0 ? (
            <EmptyState compact className="py-6" title={t('summary.noEvents')} />
          ) : (
            <OutcomeDonut slices={slices} height={180} />
          )}
        </CardContent>
      </Card>
    </section>
  );
}
