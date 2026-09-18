import { useTranslation } from 'react-i18next';

import { EmptyState } from '@/components/EmptyState';
import { IdText } from '@/components/CopyButton';
import { StateBadge } from '@/components/StateBadge';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { type ProxySiteState } from '@/gen/spinneret/v1/proxy_admin_pb';
import { formatNumber } from '@/lib/format';
import { cn } from '@/lib/utils';

import { Countdown } from './Countdown';

/** Health score (0..100) as a number with a small bar. */
export function ScoreBar({ score, label }: { score: number; label: string }) {
  const { i18n } = useTranslation();
  const value = Number.isFinite(score) ? Math.min(100, Math.max(0, score)) : 0;
  const tone = value >= 70 ? 'bg-emerald-500' : value >= 40 ? 'bg-amber-500' : 'bg-rose-500';
  return (
    <span className="inline-flex items-center gap-2">
      <span
        className="h-1.5 w-16 overflow-hidden rounded-full bg-muted"
        role="meter"
        aria-label={label}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={Math.round(value)}
      >
        <span className={cn('block h-full rounded-full', tone)} style={{ width: `${value}%` }} />
      </span>
      <span className="tabular w-8 text-right text-xs">
        {formatNumber(value, { maximumFractionDigits: 0 }, i18n.language)}
      </span>
    </span>
  );
}

/** Per-site hot state of a proxy: state, score, samples, active leases and cooldown countdown. */
export function ProxySiteStates({ sites }: { sites: readonly ProxySiteState[] }) {
  const { t, i18n } = useTranslation('proxies');
  if (sites.length === 0) {
    return <EmptyState compact title={t('detail.sitesEmpty')} />;
  }
  return (
    <div className="overflow-x-auto rounded-md border">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{t('detail.site')}</TableHead>
            <TableHead>{t('detail.state')}</TableHead>
            <TableHead>{t('detail.score')}</TableHead>
            <TableHead className="text-right">{t('detail.samples')}</TableHead>
            <TableHead className="text-right">{t('detail.leases')}</TableHead>
            <TableHead>{t('detail.cooldown')}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {sites.map((site) => (
            <TableRow key={site.siteId || site.site}>
              <TableCell>
                <div className="font-medium">{site.site}</div>
                <IdText value={site.siteId} truncate={12} className="text-muted-foreground" />
              </TableCell>
              <TableCell>
                <StateBadge kind="proxy" state={site.state} />
              </TableCell>
              <TableCell>
                <ScoreBar score={site.score} label={t('detail.scoreOf', { site: site.site })} />
              </TableCell>
              <TableCell className="tabular text-right">
                {formatNumber(site.samples, undefined, i18n.language)}
              </TableCell>
              <TableCell className="tabular text-right">
                {formatNumber(site.activeLeases, undefined, i18n.language)}
              </TableCell>
              <TableCell className="tabular">
                <Countdown until={site.cooldownUntil} />
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  );
}
