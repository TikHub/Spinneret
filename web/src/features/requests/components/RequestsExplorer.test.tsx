import { create } from '@bufbuild/protobuf';
import { timestampFromMs, timestampMs } from '@bufbuild/protobuf/wkt';
import { Code, ConnectError, createClient, createRouterTransport } from '@connectrpc/connect';
import { screen, waitFor, within } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import {
  DashboardService,
  QueryRequestEventsResponseSchema,
  RequestEventSchema,
  RequestEventsSummarySchema,
  type QueryRequestEventsRequest,
} from '@/gen/spinneret/v1/dashboard_pb';
import { ListSitesResponseSchema, SiteAdminService, SiteSchema } from '@/gen/spinneret/v1/site_admin_pb';
import { HOUR_MS } from '@/lib/time';

const handlers = vi.hoisted(() => ({
  queryRequestEvents: undefined as undefined | ((req: QueryRequestEventsRequest) => unknown),
}));

vi.mock('@/lib/clients', () => {
  const transport = createRouterTransport((router) => {
    router.service(DashboardService, {
      queryRequestEvents: (req) => {
        if (!handlers.queryRequestEvents) throw new ConnectError('no handler', Code.Unimplemented);
        return handlers.queryRequestEvents(req) as ReturnType<
          typeof create<typeof QueryRequestEventsResponseSchema>
        >;
      },
    });
    router.service(SiteAdminService, {
      listSites: () =>
        create(ListSitesResponseSchema, {
          sites: [create(SiteSchema, { id: 'sit_1', name: 'shop', clients: ['web'] })],
          total: 1,
        }),
    });
  });
  return {
    dashboardClient: createClient(DashboardService, transport),
    siteClient: createClient(SiteAdminService, transport),
  };
});
vi.mock('@/app/auth/AuthContext', async () => (await import('../shared/testing')).authModuleMock());
vi.mock('@/app/theme/ThemeProvider', () => ({ useTheme: () => ({ resolvedTheme: 'light' }) }));
vi.mock('@/components/charts/EChart', () => ({ EChart: () => <div data-testid="echart" /> }));

const { renderWithRouter } = await import('../shared/testing');
const { DEFAULT_REQUEST_FILTERS } = await import('../requestFilters');
const { RequestsExplorer } = await import('./RequestsExplorer');

function Explorer() {
  return <RequestsExplorer filters={DEFAULT_REQUEST_FILTERS} onFiltersChange={() => undefined} />;
}

afterEach(() => {
  handlers.queryRequestEvents = undefined;
});

describe('RequestsExplorer', () => {
  it('explains how to enable ClickHouse when the explorer is unavailable', async () => {
    const calls = vi.fn();
    handlers.queryRequestEvents = () => {
      calls();
      throw new ConnectError('clickhouse is not configured', Code.Unavailable);
    };
    renderWithRouter(Explorer);

    expect(await screen.findByText('clickhouse.title')).toBeInTheDocument();
    expect(screen.getByText(/SPINNERET_CLICKHOUSE_URL=/)).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /clickhouse.openRiskEvents/ })).toHaveAttribute(
      'href',
      '/risk-events',
    );
    expect(screen.queryByText('summary.total')).not.toBeInTheDocument();
    // Unavailable is not retried.
    expect(calls).toHaveBeenCalledTimes(1);
  });

  it('requests the first page with a summary and renders events', async () => {
    const requests: QueryRequestEventsRequest[] = [];
    handlers.queryRequestEvents = (req) => {
      requests.push(req);
      return create(QueryRequestEventsResponseSchema, {
        events: [
          create(RequestEventSchema, {
            eventTime: timestampFromMs(Date.now() - 60_000),
            site: 'shop',
            client: 'web',
            endpointGroup: 'search',
            identityId: 'idn_1',
            leaseId: 'lse_1',
            reportId: 'rpt_1',
            outcome: 'captcha',
            httpStatus: 403,
            latencyMs: 850,
            probe: true,
          }),
        ],
        summary: req.includeSummary
          ? create(RequestEventsSummarySchema, {
              total: 3n,
              outcomes: { success: 2n, captcha: 1n },
              latencyP95Ms: 1200,
            })
          : undefined,
      });
    };
    renderWithRouter(Explorer);

    const table = await screen.findByRole('table');
    await waitFor(() => expect(table.querySelector('td [data-outcome="captcha"]')).not.toBeNull());
    const [req] = requests;
    expect(req).toMatchObject({ namespace: 'default', includeSummary: true, pageToken: '', pageSize: 50 });
    const start = req?.timeRange?.start;
    const end = req?.timeRange?.end;
    expect(start && end && timestampMs(end) - timestampMs(start)).toBe(HOUR_MS);

    const total = screen.getByText('summary.total').closest('div')?.parentElement;
    expect(total && within(total).getByText('3')).toBeInTheDocument();
    await waitFor(() =>
      expect(screen.getByRole('link', { name: 'shared.openIdentity' })).toHaveAttribute(
        'href',
        '/identities/idn_1',
      ),
    );
    expect(screen.getByText('flags.probe')).toBeInTheDocument();
    expect(screen.getByTestId('echart')).toBeInTheDocument();
  });
});
