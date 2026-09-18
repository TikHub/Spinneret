import { useNavigate, useSearch } from '@tanstack/react-router';
import { useCallback, useMemo } from 'react';

import { PERMISSIONS } from '@/app/auth/permissions';
import { RequirePermission } from '@/app/auth/PermissionGate';
import { PageIntro, PageIntroSlot } from '@/components/PageIntro';

import { RequestsExplorer } from '../components/RequestsExplorer';
import { parseRequestFilters, requestFiltersToSearch, type RequestFilters } from '../requestFilters';

function RequestsRoute() {
  const search = useSearch({ from: '/_app/requests' });
  const navigate = useNavigate({ from: '/requests' });
  const filters = useMemo(() => parseRequestFilters(search), [search]);
  const onFiltersChange = useCallback(
    (next: RequestFilters) => void navigate({ search: requestFiltersToSearch(next), replace: true }),
    [navigate],
  );
  // RequestsExplorer renders the PageHeader, so the intro card is slotted under it.
  return (
    <PageIntroSlot
      intro={
        <PageIntro
          page="requests"
          links={[
            { to: '/risk-events', labelKey: 'nav.riskEvents' },
            { to: '/heatmap', labelKey: 'nav.heatmap' },
          ]}
        />
      }
    >
      <RequestsExplorer filters={filters} onFiltersChange={onFiltersChange} />
    </PageIntroSlot>
  );
}

/** Request explorer (ClickHouse report events) with URL-synced filters. */
export default function RequestsPage() {
  return (
    <RequirePermission permission={PERMISSIONS.dashboardRead}>
      <RequestsRoute />
    </RequirePermission>
  );
}
