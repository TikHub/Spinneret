import { useNavigate, useSearch } from '@tanstack/react-router';
import { useCallback, useMemo } from 'react';

import { PERMISSIONS } from '@/app/auth/permissions';
import { RequirePermission } from '@/app/auth/PermissionGate';
import { PageIntro, PageIntroSlot } from '@/components/PageIntro';

import { HeatmapView } from '../components/HeatmapView';
import { heatmapSelectionToSearch, parseHeatmapSearch, type HeatmapSelection } from '../heatmapSearch';

function HeatmapRoute() {
  const search = useSearch({ from: '/_app/heatmap' });
  const navigate = useNavigate({ from: '/heatmap' });
  const selection = useMemo(() => parseHeatmapSearch(search), [search]);
  const onSelectionChange = useCallback(
    (next: HeatmapSelection) => void navigate({ search: heatmapSelectionToSearch(next), replace: true }),
    [navigate],
  );
  // HeatmapView renders the PageHeader, so the intro card is slotted under it.
  return (
    <PageIntroSlot
      intro={
        <PageIntro
          page="heatmap"
          links={[
            { to: '/identities', labelKey: 'nav.identities' },
            { to: '/breakers', labelKey: 'nav.breakers' },
          ]}
        />
      }
    >
      <HeatmapView selection={selection} onSelectionChange={onSelectionChange} />
    </PageIntroSlot>
  );
}

/** Identity × endpoint group heatmap (remaining cooldown or health score) with drill-down. */
export default function HeatmapPage() {
  return (
    <RequirePermission permission={PERMISSIONS.dashboardRead}>
      <HeatmapRoute />
    </RequirePermission>
  );
}
