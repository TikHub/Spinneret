import { create } from '@bufbuild/protobuf';
import { Code, ConnectError, createClient, createRouterTransport } from '@connectrpc/connect';
import { act, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import {
  DashboardService,
  GetHeatmapResponseSchema,
  HeatmapCellSchema,
  HeatmapRowSchema,
  type GetHeatmapRequest,
  type GetHeatmapResponse,
} from '@/gen/spinneret/v1/dashboard_pb';
import { ListSitesResponseSchema, SiteAdminService, SiteSchema } from '@/gen/spinneret/v1/site_admin_pb';

const state = vi.hoisted(() => ({
  getHeatmap: undefined as undefined | ((req: GetHeatmapRequest) => GetHeatmapResponse),
  chartEvents: undefined as undefined | Record<string, (params: unknown) => void>,
}));

vi.mock('@/lib/clients', () => {
  const transport = createRouterTransport((router) => {
    router.service(DashboardService, {
      getHeatmap: (req) => {
        if (!state.getHeatmap) throw new ConnectError('no handler', Code.Unimplemented);
        return state.getHeatmap(req);
      },
    });
    router.service(SiteAdminService, {
      listSites: () =>
        create(ListSitesResponseSchema, {
          sites: [
            create(SiteSchema, { id: 'sit_1', name: 'shop', clients: ['web', 'app'] }),
            create(SiteSchema, { id: 'sit_2', name: 'news', clients: ['web'] }),
          ],
        }),
    });
  });
  return {
    dashboardClient: createClient(DashboardService, transport),
    siteClient: createClient(SiteAdminService, transport),
  };
});
vi.mock('@/app/auth/AuthContext', async () =>
  (await import('@/features/requests/shared/testing')).authModuleMock(),
);
vi.mock('@/app/theme/ThemeProvider', () => ({ useTheme: () => ({ resolvedTheme: 'light' }) }));
vi.mock('@/components/charts/EChart', () => ({
  EChart: ({ onEvents }: { onEvents?: Record<string, (params: unknown) => void> }) => {
    state.chartEvents = onEvents;
    return <div data-testid="echart" />;
  },
}));

const { renderWithRouter } = await import('@/features/requests/shared/testing');
const { DEFAULT_HEATMAP_SELECTION } = await import('../heatmapSearch');
const { HeatmapView } = await import('./HeatmapView');

function heatmap() {
  return create(GetHeatmapResponseSchema, {
    columns: ['search', 'detail'],
    columnIds: ['eg_1', 'eg_2'],
    rows: [create(HeatmapRowSchema, { identityId: 'idn_1', label: 'acct-1', state: 'active' })],
    cells: [
      create(HeatmapCellSchema, { row: 0, col: 0, score: 40, cooldownRemainingMs: 125_000n }),
      // Permanent bans are reported as the largest int64.
      create(HeatmapCellSchema, { row: 0, col: 1, score: 0, cooldownRemainingMs: 9223372036854775807n }),
    ],
    total: 1,
  });
}

afterEach(() => {
  state.getHeatmap = undefined;
  state.chartEvents = undefined;
});

describe('HeatmapView', () => {
  it('defaults to the first site and client and drills down on click', async () => {
    const requests: GetHeatmapRequest[] = [];
    state.getHeatmap = (req) => {
      requests.push(req);
      return heatmap();
    };
    const { router } = renderWithRouter(() => (
      <HeatmapView selection={DEFAULT_HEATMAP_SELECTION} onSelectionChange={() => undefined} />
    ));

    await screen.findByTestId('echart');
    expect(requests[0]).toMatchObject({
      namespace: 'default',
      site: 'shop',
      client: 'web',
      metric: 'cooldown',
      states: ['active', 'pending'],
      limit: 100,
      pageToken: '',
    });
    await waitFor(() => expect(state.chartEvents?.click).toBeDefined());
    act(() => state.chartEvents?.click?.({ value: [1, 0, 0] }));
    await waitFor(() => expect(router.state.location.pathname).toBe('/identities/idn_1'));
  });

  it('renders the accessible table view with cell values', async () => {
    state.getHeatmap = () => heatmap();
    renderWithRouter(() => (
      <HeatmapView
        selection={{ ...DEFAULT_HEATMAP_SELECTION, site: 'shop', client: 'app', view: 'table' }}
        onSelectionChange={() => undefined}
      />
    ));

    const link = await screen.findByRole('link', { name: 'acct-1' });
    expect(link).toHaveAttribute('href', '/identities/idn_1');
    // No translations are loaded here: the localized cooldown (2m 5s) renders as its unit keys.
    expect(
      screen.getByText('common:duration.units.minutes common:duration.units.seconds'),
    ).toBeInTheDocument();
    expect(screen.getByText('common:duration.permanent')).toBeInTheDocument();
    expect(screen.queryByTestId('echart')).not.toBeInTheDocument();
  });

  it('shows an empty state without matching identities', async () => {
    state.getHeatmap = () => create(GetHeatmapResponseSchema, { columns: ['search'], columnIds: ['eg_1'] });
    renderWithRouter(() => (
      <HeatmapView selection={DEFAULT_HEATMAP_SELECTION} onSelectionChange={() => undefined} />
    ));
    expect(await screen.findByText('emptyTitle')).toBeInTheDocument();
  });
});
