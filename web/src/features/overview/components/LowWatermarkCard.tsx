import { useTranslation } from 'react-i18next';

import { EmptyState } from '@/components/EmptyState';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { type SiteOverview } from '@/gen/spinneret/v1/dashboard_pb';
import { formatNumber } from '@/lib/format';

/** Endpoint groups below their low watermark, across all sites. */
export function LowWatermarkCard({ sites }: { sites: readonly SiteOverview[] }) {
  const { t, i18n } = useTranslation();
  const warnings = sites.flatMap((site) => site.lowWatermarkWarnings.map((w) => ({ site: site.site, ...w })));

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('overview.lowWatermark.title')}</CardTitle>
      </CardHeader>
      <CardContent>
        {warnings.length === 0 ? (
          <EmptyState compact title={t('overview.lowWatermark.empty')} />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b text-xs text-muted-foreground">
                  <th className="py-1.5 pr-3 text-left font-medium">{t('overview.lowWatermark.site')}</th>
                  <th className="py-1.5 pr-3 text-left font-medium">{t('overview.lowWatermark.group')}</th>
                  <th className="py-1.5 pr-3 text-left font-medium">{t('overview.lowWatermark.client')}</th>
                  <th className="py-1.5 pr-3 text-right font-medium">
                    {t('overview.lowWatermark.available')}
                  </th>
                  <th className="py-1.5 text-right font-medium">{t('overview.lowWatermark.threshold')}</th>
                </tr>
              </thead>
              <tbody>
                {warnings.map((w) => (
                  <tr key={`${w.site}-${w.endpointGroupId}-${w.client}`} className="border-b last:border-0">
                    <td className="py-1.5 pr-3">{w.site}</td>
                    <td className="py-1.5 pr-3">{w.endpointGroup}</td>
                    <td className="py-1.5 pr-3 font-mono text-xs">{w.client}</td>
                    <td className="tabular py-1.5 pr-3 text-right font-medium text-amber-600 dark:text-amber-400">
                      {formatNumber(w.available, undefined, i18n.language)}
                    </td>
                    <td className="tabular py-1.5 text-right text-muted-foreground">
                      {formatNumber(w.lowWatermark, undefined, i18n.language)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </CardContent>
    </Card>
  );
}
