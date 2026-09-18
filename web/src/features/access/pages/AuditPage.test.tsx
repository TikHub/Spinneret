import { timestampMs } from '@bufbuild/protobuf/wkt';
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from '@tanstack/react-router';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { type ListAuditLogsRequest } from '@/gen/spinneret/v1/access_admin_pb';
import { DAY_MS, HOUR_MS } from '@/lib/time';

const api = vi.hoisted(() => ({ getMe: vi.fn(), listAuditLogs: vi.fn() }));

vi.mock('@/lib/clients', async () => {
  const { createClient, createRouterTransport } = await import('@connectrpc/connect');
  const { AuthService } = await import('@/gen/spinneret/v1/auth_pb');
  const { AccessAdminService } = await import('@/gen/spinneret/v1/access_admin_pb');
  const transport = createRouterTransport((router) => {
    router.service(AuthService, { getMe: () => api.getMe() });
    router.service(AccessAdminService, { listAuditLogs: (req) => api.listAuditLogs(req) });
  });
  return {
    authClient: createClient(AuthService, transport),
    accessClient: createClient(AccessAdminService, transport),
  };
});

const { meResponse, renderWithProviders } = await import('../testing/providers');
const { default: AuditPage } = await import('./AuditPage');

/** Minimal route tree with the same route ID as the app ("/_app/access/audit"). */
function createTestRouter(initialPath: string) {
  const root = createRootRoute({ component: Outlet });
  const app = createRoute({ getParentRoute: () => root, id: '_app', component: Outlet });
  const audit = createRoute({
    getParentRoute: () => app,
    path: 'access/audit',
    validateSearch: (search: Record<string, unknown>) => search,
    component: AuditPage,
  });
  return createRouter({
    routeTree: root.addChildren([app.addChildren([audit])]),
    history: createMemoryHistory({ initialEntries: [initialPath] }),
  });
}

const log = {
  id: 'aud_1',
  namespace: 'prod',
  actorKind: 'user',
  actorId: 'usr_me',
  actorName: 'me',
  action: 'secret.read',
  resourceKind: 'secret',
  resourceId: 'sec_1',
  resourceName: 'prod/signing/api_key',
  result: 'denied',
  ip: '203.0.113.7',
  userAgent: 'Mozilla/5.0',
  details: { reason: 'scope_missing', attempts: 2 },
};

describe('AuditPage', () => {
  beforeEach(() => {
    window.localStorage.clear();
    api.getMe.mockResolvedValue(meResponse({ permissions: ['audit:read'] }));
    api.listAuditLogs.mockResolvedValue({ logs: [log], nextPageToken: '' });
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it('applies filters from the URL and expands rows to show details', async () => {
    const user = userEvent.setup();
    const router = createTestRouter('/access/audit?result=denied&actor=me&range=1h');
    renderWithProviders(<RouterProvider router={router} />);

    expect(await screen.findByText('secret.read')).toBeInTheDocument();
    const request = api.listAuditLogs.mock.calls[0]?.[0] as ListAuditLogsRequest;
    expect(request).toMatchObject({ result: 'denied', actor: 'me', namespace: '' });
    const { start, end } = request.timeRange ?? {};
    expect(start && Date.now() - timestampMs(start)).toBeGreaterThanOrEqual(HOUR_MS);
    expect(start && Date.now() - timestampMs(start)).toBeLessThan(HOUR_MS + 60_000);
    expect(end).toBeUndefined();

    expect(screen.queryByText('"scope_missing"')).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Show details' }));
    expect(await screen.findByText('"scope_missing"')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Hide details' })).toHaveAttribute('aria-expanded', 'true');
  });

  it('writes filter changes to the URL and refetches', async () => {
    const user = userEvent.setup();
    const router = createTestRouter('/access/audit');
    renderWithProviders(<RouterProvider router={router} />);

    await screen.findByText('secret.read');
    const first = api.listAuditLogs.mock.calls[0]?.[0] as ListAuditLogsRequest;
    const { start, end } = first.timeRange ?? {};
    expect(start && Date.now() - timestampMs(start)).toBeGreaterThanOrEqual(DAY_MS);
    expect(start && Date.now() - timestampMs(start)).toBeLessThan(DAY_MS + 60_000);
    expect(end).toBeUndefined();

    await user.type(screen.getByRole('textbox', { name: 'Resource ID' }), 'sec_42');
    await waitFor(() => expect(router.state.location.search).toMatchObject({ resourceId: 'sec_42' }));
    await waitFor(() =>
      expect(api.listAuditLogs).toHaveBeenLastCalledWith(expect.objectContaining({ resourceId: 'sec_42' })),
    );

    await user.click(screen.getByRole('button', { name: /Reset filters/ }));
    await waitFor(() => expect(router.state.location.search).toEqual({}));
    expect(within(screen.getByRole('search')).getByRole('textbox', { name: 'Resource ID' })).toHaveValue('');
  });
});
