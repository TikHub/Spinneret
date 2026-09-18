import { TriangleAlertIcon } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { StateBadge } from '@/components/StateBadge';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { SimpleTooltip } from '@/components/ui/tooltip';
import { type SiteOverview } from '@/gen/spinneret/v1/dashboard_pb';
import { formatNumber, formatPercent, formatRate } from '@/lib/format';
import { STATE_TONE_DOT_CLASS, stateTone } from '@/lib/states';
import { cn } from '@/lib/utils';

const IDENTITY_STATE_ORDER = [
  'active',
  'pending',
  'expired',
  'quarantined',
  'banned',
  'disabled',
  'retired',
] as const;

function Metric({ label, value, tone }: { label: string; value: string; tone?: 'good' | 'bad' | 'warn' }) {
  return (
    <div className="min-w-0">
      <div className="truncate text-[11px] text-muted-foreground">{label}</div>
      <div
        className={cn(
          'tabular text-sm font-semibold',
          tone === 'good' && 'text-emerald-600 dark:text-emerald-400',
          tone === 'bad' && 'text-rose-600 dark:text-rose-400',
          tone === 'warn' && 'text-amber-600 dark:text-amber-400',
        )}
      >
        {value}
      </div>
    </div>
  );
}

/** Stacked bar of identity counts by lifecycle state. */
function StateBar({ counts }: { counts: Record<string, number> }) {
  const { t, i18n } = useTranslation();
  const entries = IDENTITY_STATE_ORDER.map((state) => [state, counts[state] ?? 0] as const).filter(
    ([, n]) => n > 0,
  );
  const total = entries.reduce((sum, [, n]) => sum + n, 0);
  if (total === 0) return <div className="h-1.5 rounded-full bg-muted" />;
  return (
    <div className="space-y-1.5">
      <div className="flex h-1.5 overflow-hidden rounded-full bg-muted">
        {entries.map(([state, n]) => (
          <SimpleTooltip
            key={state}
            content={`${t(`states.${state}`)}: ${formatNumber(n, undefined, i18n.language)}`}
          >
            <div
              className={STATE_TONE_DOT_CLASS[stateTone('identity', state)]}
              style={{ width: `${(n / total) * 100}%` }}
            />
          </SimpleTooltip>
        ))}
      </div>
      <div className="flex flex-wrap gap-x-3 gap-y-0.5 text-[11px] text-muted-foreground">
        {entries.map(([state, n]) => (
          <span key={state} className="inline-flex items-center gap-1">
            <span
              className={cn('size-1.5 rounded-full', STATE_TONE_DOT_CLASS[stateTone('identity', state)])}
            />
            {t(`states.${state}`)}{' '}
            <span className="tabular text-foreground">{formatNumber(n, undefined, i18n.language)}</span>
          </span>
        ))}
      </div>
    </div>
  );
}

/** Per-site overview card. */
export function SiteCard({ site }: { site: SiteOverview }) {
  const { t, i18n } = useTranslation();
  const lng = i18n.language;
  const lowWatermarks = site.lowWatermarkWarnings.length;

  return (
    <Card>
      <CardHeader className="flex-row items-start justify-between gap-2">
        <div className="min-w-0">
          <CardTitle className="truncate">{site.displayName || site.site}</CardTitle>
          <div className="mt-1 truncate font-mono text-xs text-muted-foreground">{site.site}</div>
        </div>
        <div className="flex shrink-0 flex-wrap justify-end gap-1">
          {site.paused && <StateBadge kind="site" state="paused" />}
          {site.openBreakers > 0 && (
            <StateBadge
              kind="breaker"
              state="open"
              label={t('overview.site.breakersOpen', { count: site.openBreakers })}
            />
          )}
          {site.halfOpenBreakers > 0 && (
            <StateBadge
              kind="breaker"
              state="half_open"
              label={t('overview.site.breakersHalfOpen', { count: site.halfOpenBreakers })}
            />
          )}
        </div>
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="flex items-end justify-between gap-3">
          <div>
            <div className="text-[11px] text-muted-foreground">{t('overview.site.available')}</div>
            <div className="tabular text-2xl leading-tight font-semibold">
              {formatNumber(site.availableIdentities, undefined, lng)}
            </div>
          </div>
          <div className="grid grid-cols-2 gap-x-4 text-right">
            <Metric label={t('overview.site.acquireQps')} value={formatRate(site.acquireQps, lng)} />
            <Metric label={t('overview.site.reportQps')} value={formatRate(site.reportQps, lng)} />
          </div>
        </div>
        <div className="grid grid-cols-4 gap-2 rounded-md bg-muted/40 px-2.5 py-2">
          <Metric
            label={t('overview.site.success')}
            value={formatPercent(site.successRatio, 1, lng)}
            tone={site.reportQps <= 0 ? undefined : site.successRatio < 0.8 ? 'warn' : 'good'}
          />
          <Metric
            label={t('overview.site.risk')}
            value={formatPercent(site.riskRatio, 1, lng)}
            tone={site.riskRatio >= 0.1 ? 'bad' : undefined}
          />
          <Metric label={t('overview.site.unknown')} value={formatPercent(site.unknownRatio, 1, lng)} />
          <Metric
            label={t('overview.site.acquireFailures')}
            value={formatPercent(site.acquireFailureRatio, 1, lng)}
            tone={site.acquireFailureRatio >= 0.05 ? 'warn' : undefined}
          />
        </div>
        <div>
          <div className="mb-1 text-[11px] text-muted-foreground">{t('overview.site.identities')}</div>
          <StateBar counts={site.identitiesByState} />
        </div>
        {lowWatermarks > 0 && (
          <div className="flex items-center gap-1.5 text-xs text-amber-600 dark:text-amber-400">
            <TriangleAlertIcon className="size-3.5" aria-hidden />
            {t('overview.site.lowWatermark', { count: lowWatermarks })}
          </div>
        )}
      </CardContent>
    </Card>
  );
}
