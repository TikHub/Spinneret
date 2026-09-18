import { Link } from '@tanstack/react-router';
import { useTranslation } from 'react-i18next';

import { EmptyState } from '@/components/EmptyState';
import { ErrorState } from '@/components/ErrorState';
import { StateBadge } from '@/components/StateBadge';
import { TimeAgo } from '@/components/TimeAgo';
import { Button } from '@/components/ui/button';
import { Card, CardAction, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import { type BreakerStatus } from '@/gen/spinneret/v1/breaker_admin_pb';

export interface OpenBreakersCardProps {
  breakers: readonly BreakerStatus[] | undefined;
  total: number | undefined;
  isLoading: boolean;
  error: unknown;
  onRetry: () => void;
}

/** Open and half-open breakers of the namespace. */
export function OpenBreakersCard({ breakers, total, isLoading, error, onRetry }: OpenBreakersCardProps) {
  const { t } = useTranslation();
  return (
    <Card>
      <CardHeader className="flex-row items-center">
        <CardTitle>
          {t('overview.breakers.title')}
          {total !== undefined && total > 0 && (
            <span className="ml-1.5 text-muted-foreground">({total})</span>
          )}
        </CardTitle>
        <CardAction>
          <Button variant="link" size="sm" className="h-auto p-0" asChild>
            <Link to="/breakers">{t('actions.viewAll')}</Link>
          </Button>
        </CardAction>
      </CardHeader>
      <CardContent>
        {isLoading && !breakers ? (
          <div className="space-y-2">
            {Array.from({ length: 3 }, (_, i) => (
              <Skeleton key={i} className="h-8 w-full" />
            ))}
          </div>
        ) : error && !breakers ? (
          <ErrorState error={error} onRetry={onRetry} compact />
        ) : !breakers || breakers.length === 0 ? (
          <EmptyState compact title={t('overview.breakers.empty')} />
        ) : (
          <ul className="divide-y">
            {breakers.map((b) => (
              <li key={b.endpointGroupId} className="flex items-center justify-between gap-3 py-2">
                <div className="min-w-0">
                  <div className="truncate text-sm font-medium">{b.endpointGroup}</div>
                  <div className="truncate text-xs text-muted-foreground">
                    {b.site} · {b.client}
                    {b.manual && ` · ${t('overview.breakers.manual')}`}
                    {b.reason && ` · ${b.reason}`}
                  </div>
                </div>
                <div className="flex shrink-0 flex-col items-end gap-1">
                  <StateBadge kind="breaker" state={b.state} />
                  {b.state === 'open' && (
                    <span className="text-xs text-muted-foreground">
                      {b.openUntil ? <TimeAgo value={b.openUntil} /> : t('overview.breakers.indefinite')}
                    </span>
                  )}
                </div>
              </li>
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  );
}
