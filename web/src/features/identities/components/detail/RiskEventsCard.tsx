import { useQuery } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { LockIcon, ShieldCheckIcon } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { useAuth, useScopedQueryKey } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';
import { IdText } from '@/components/CopyButton';
import { EmptyState } from '@/components/EmptyState';
import { ErrorState } from '@/components/ErrorState';
import { TimeAgo } from '@/components/TimeAgo';
import { Badge } from '@/components/ui/badge';
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import { dashboardClient } from '@/lib/clients';
import { formatLatency } from '@/lib/format';

const RISK_EVENTS_LIMIT = 20;

/** Recent non-success reports of the identity (DashboardService.ListRiskEvents, last 24 hours). */
export function RiskEventsCard({ identityId, site }: { identityId: string; site: string }) {
  const { t } = useTranslation('identities');
  const { namespaceName, can } = useAuth();
  const key = useScopedQueryKey();
  // dashboard:read may be granted per site; check the identity's site.
  const allowed = can(PERMISSIONS.dashboardRead, site || undefined);

  const query = useQuery({
    queryKey: key('risk-events', 'identity', identityId),
    queryFn: ({ signal }) =>
      dashboardClient.listRiskEvents(
        { namespace: namespaceName ?? '', identityId, pageSize: RISK_EVENTS_LIMIT },
        { signal },
      ),
    enabled: allowed && Boolean(namespaceName),
  });
  const events = query.data?.events ?? [];
  const head = [
    t('risk.time'),
    t('risk.endpointGroup'),
    t('risk.outcome'),
    t('risk.blame'),
    t('risk.status'),
    t('risk.rule'),
    t('risk.proxy'),
    t('risk.node'),
    t('risk.latency'),
  ];

  return (
    <Card>
      <CardHeader className="flex-row items-start">
        <div className="grid gap-1">
          <CardTitle>{t('risk.title')}</CardTitle>
          <CardDescription>{t('risk.description')}</CardDescription>
        </div>
        {allowed && (
          <CardAction>
            <Link
              to="/risk-events"
              search={{ identity_id: identityId }}
              className="text-xs text-primary hover:underline"
            >
              {t('common:actions.viewAll')}
            </Link>
          </CardAction>
        )}
      </CardHeader>
      <CardContent>
        {!allowed ? (
          <EmptyState
            compact
            icon={LockIcon}
            title={t('common:permission.deniedTitle')}
            description={t('common:permission.missing', { permission: PERMISSIONS.dashboardRead })}
          />
        ) : query.isLoading ? (
          <div className="grid gap-2">
            {Array.from({ length: 3 }, (_, i) => (
              <Skeleton key={i} className="h-8 w-full" />
            ))}
          </div>
        ) : query.isError ? (
          <ErrorState compact error={query.error} onRetry={() => void query.refetch()} />
        ) : events.length === 0 ? (
          <EmptyState compact icon={ShieldCheckIcon} title={t('risk.empty')} />
        ) : (
          <div className="overflow-x-auto rounded-md border">
            <table className="w-full text-sm">
              <thead className="bg-muted/60">
                <tr>
                  {head.map((label) => (
                    <th
                      key={label}
                      scope="col"
                      className="h-8 px-3 text-left text-xs font-medium whitespace-nowrap text-muted-foreground"
                    >
                      {label}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {events.map((event) => (
                  <tr key={event.id} className="border-t">
                    <td className="px-3 py-2 whitespace-nowrap">
                      <TimeAgo value={event.createdAt} past />
                    </td>
                    <td className="px-3 py-2 font-mono text-xs whitespace-nowrap">
                      {event.endpointGroup || '—'}
                    </td>
                    <td className="px-3 py-2">
                      <Badge variant="outline" className="font-mono font-normal">
                        {event.outcome}
                      </Badge>
                    </td>
                    <td className="px-3 py-2 text-xs">{event.blame || '—'}</td>
                    <td className="tabular px-3 py-2 text-xs">
                      {event.httpStatus || '—'}
                      {event.businessCode && (
                        <span className="text-muted-foreground"> / {event.businessCode}</span>
                      )}
                    </td>
                    <td className="px-3 py-2 font-mono text-xs">{event.rule || '—'}</td>
                    <td className="px-3 py-2">
                      <IdText value={event.proxyId} truncate={14} />
                    </td>
                    <td className="px-3 py-2 text-xs whitespace-nowrap">{event.node || '—'}</td>
                    <td className="tabular px-3 py-2 text-xs whitespace-nowrap">
                      {formatLatency(event.latencyMs)}
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
