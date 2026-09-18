import { create } from '@bufbuild/protobuf';
import { timestampFromMs, timestampMs } from '@bufbuild/protobuf/wkt';
import { Code, ConnectError, createClient, createRouterTransport } from '@connectrpc/connect';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import {
  DashboardService,
  ListRiskEventsResponseSchema,
  RiskEventSchema,
  type ListRiskEventsRequest,
  type ListRiskEventsResponse,
} from '@/gen/spinneret/v1/dashboard_pb';
import { ListSitesResponseSchema, SiteAdminService } from '@/gen/spinneret/v1/site_admin_pb';
import { DAY_MS } from '@/lib/time';

const handlers = vi.hoisted(() => ({
  listRiskEvents: undefined as undefined | ((req: ListRiskEventsRequest) => ListRiskEventsResponse),
  canReadIdentities: true,
}));

vi.mock('@/lib/clients', () => {
  const transport = createRouterTransport((router) => {
    router.service(DashboardService, {
      listRiskEvents: (req) => {
        if (!handlers.listRiskEvents) throw new ConnectError('no handler', Code.Unimplemented);
        return handlers.listRiskEvents(req);
      },
    });
    router.service(SiteAdminService, { listSites: () => create(ListSitesResponseSchema, {}) });
  });
  return {
    dashboardClient: createClient(DashboardService, transport),
    siteClient: createClient(SiteAdminService, transport),
  };
});
vi.mock('@/app/auth/AuthContext', async () =>
  (await import('@/features/requests/shared/testing')).authModuleMock(
    (permission) => handlers.canReadIdentities || permission !== 'identity:read',
  ),
);

const { renderWithRouter } = await import('@/features/requests/shared/testing');
const { DEFAULT_RISK_FILTERS } = await import('../riskFilters');
const { RiskEventsView } = await import('./RiskEventsPage');

function View() {
  return (
    <RiskEventsView
      filters={{ ...DEFAULT_RISK_FILTERS, outcome: 'banned' }}
      onFiltersChange={() => undefined}
    />
  );
}

afterEach(() => {
  handlers.listRiskEvents = undefined;
  handlers.canReadIdentities = true;
});

function riskEvent() {
  return create(RiskEventSchema, {
    id: 'rsk_1',
    createdAt: timestampFromMs(Date.now() - 5_000),
    site: 'shop',
    client: 'web',
    endpointGroup: 'search',
    identityId: 'idn_7',
    outcome: 'banned',
    blame: 'identity',
    uri: '/api/search?q=shoes',
    method: 'GET',
    startedAt: timestampFromMs(Date.now() - 7_000),
    finishedAt: timestampFromMs(Date.now() - 6_000),
  });
}

describe('RiskEventsView', () => {
  it('lists events for the last 24 hours and expands a row', async () => {
    const requests: ListRiskEventsRequest[] = [];
    handlers.listRiskEvents = (req) => {
      requests.push(req);
      return create(ListRiskEventsResponseSchema, { events: [riskEvent()], nextPageToken: 'next' });
    };
    renderWithRouter(View);

    const expand = await screen.findByRole('button', { name: 'expandRow' });
    const [req] = requests;
    expect(req).toMatchObject({ namespace: 'default', outcome: 'banned', pageSize: 50, pageToken: '' });
    const start = req?.timeRange?.start;
    const end = req?.timeRange?.end;
    expect(start && end && timestampMs(end) - timestampMs(start)).toBe(DAY_MS);
    expect(screen.getByRole('link', { name: 'shared.openIdentity' })).toHaveAttribute(
      'href',
      '/identities/idn_7',
    );
    expect(screen.queryByText('/api/search?q=shoes')).not.toBeInTheDocument();

    fireEvent.click(expand);
    expect(await screen.findByText('/api/search?q=shoes')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'collapseRow' })).toHaveAttribute('aria-expanded', 'true');

    fireEvent.click(screen.getByRole('button', { name: 'table.nextPage' }));
    await waitFor(() => expect(requests.at(-1)?.pageToken).toBe('next'));
  });

  it('shows identity IDs without a link when identities cannot be read', async () => {
    handlers.canReadIdentities = false;
    handlers.listRiskEvents = () => create(ListRiskEventsResponseSchema, { events: [riskEvent()] });
    renderWithRouter(View);

    expect(await screen.findByText('idn_7')).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'shared.openIdentity' })).not.toBeInTheDocument();
  });

  it('shows the empty state', async () => {
    handlers.listRiskEvents = () => create(ListRiskEventsResponseSchema, {});
    renderWithRouter(View);
    expect(await screen.findByText('empty.title')).toBeInTheDocument();
  });
});
