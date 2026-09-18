import { useNavigate, useSearch } from '@tanstack/react-router';
import { RefreshCwIcon } from 'lucide-react';
import { useCallback, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';

import { useAuth } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';
import { RequirePermission } from '@/app/auth/PermissionGate';
import { LIVE_REFETCH_MS } from '@/app/queryClient';
import { EmptyState } from '@/components/EmptyState';
import { PageHeader } from '@/components/PageHeader';
import { PageIntro } from '@/components/PageIntro';
import { Button } from '@/components/ui/button';
import { Label } from '@/components/ui/label';
import { Switch } from '@/components/ui/switch';

import { RiskEventsTable } from '../components/RiskEventsTable';
import { RiskFiltersBar } from '../components/RiskFiltersBar';
import { parseRiskFilters, riskFiltersToSearch, type RiskFilters } from '../riskFilters';
import { useRiskEvents } from '../useRiskEvents';

export interface RiskEventsViewProps {
  filters: RiskFilters;
  onFiltersChange: (filters: RiskFilters) => void;
}

/** Risk event list for given filters (router independent). */
export function RiskEventsView({ filters, onFiltersChange }: RiskEventsViewProps) {
  const { t } = useTranslation('risk-events');
  const { namespaceName } = useAuth();
  const [autoRefresh, setAutoRefresh] = useState(false);
  const { pager, events, refresh } = useRiskEvents(filters, autoRefresh);

  if (!namespaceName) return <EmptyState title={t('noNamespace')} />;

  return (
    <>
      <PageHeader
        title={t('title')}
        description={t('description')}
        actions={
          <>
            <div className="flex items-center gap-2">
              <Switch id="risk-events-auto-refresh" checked={autoRefresh} onCheckedChange={setAutoRefresh} />
              <Label htmlFor="risk-events-auto-refresh" className="text-xs font-normal text-muted-foreground">
                {t('autoRefresh', { seconds: LIVE_REFETCH_MS / 1000 })}
              </Label>
            </div>
            <Button
              variant="outline"
              size="icon-sm"
              onClick={refresh}
              aria-label={t('common:actions.refresh')}
            >
              <RefreshCwIcon className={events.isFetching ? 'animate-spin' : undefined} />
            </Button>
          </>
        }
      />
      <PageIntro
        page="risk-events"
        links={[
          { to: '/requests', labelKey: 'nav.requests' },
          { to: '/identities', labelKey: 'nav.identities' },
        ]}
      />
      <div className="space-y-3">
        <RiskFiltersBar filters={filters} onChange={onFiltersChange} />
        <RiskEventsTable
          events={events.data?.events}
          isLoading={events.isLoading}
          isFetching={events.isFetching}
          error={events.error ?? undefined}
          onRetry={() => void events.refetch()}
          pagination={{ pager, nextPageToken: events.data?.nextPageToken }}
        />
      </div>
    </>
  );
}

function RiskEventsRoute() {
  const search = useSearch({ from: '/_app/risk-events' });
  const navigate = useNavigate({ from: '/risk-events' });
  const filters = useMemo(() => parseRiskFilters(search), [search]);
  const onFiltersChange = useCallback(
    (next: RiskFilters) => void navigate({ search: riskFiltersToSearch(next), replace: true }),
    [navigate],
  );
  return <RiskEventsView filters={filters} onFiltersChange={onFiltersChange} />;
}

/** Risk events (non-success reports stored in PostgreSQL) with URL-synced filters. */
export default function RiskEventsPage() {
  return (
    <RequirePermission permission={PERMISSIONS.dashboardRead}>
      <RiskEventsRoute />
    </RequirePermission>
  );
}
