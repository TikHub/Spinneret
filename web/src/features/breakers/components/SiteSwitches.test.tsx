import { create } from '@bufbuild/protobuf';
import { timestampFromMs } from '@bufbuild/protobuf/wkt';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { SetSitePausedResponseSchema, type SetSitePausedRequest } from '@/gen/spinneret/v1/breaker_admin_pb';
import { ListSitesResponseSchema, SiteSchema } from '@/gen/spinneret/v1/site_admin_pb';

const calls = vi.hoisted(() => ({ setSitePaused: [] as SetSitePausedRequest[], canOperate: true }));

vi.mock('@/lib/clients', async () => {
  const { createClient, createRouterTransport } = await import('@connectrpc/connect');
  const { BreakerAdminService } = await import('@/gen/spinneret/v1/breaker_admin_pb');
  const { SiteAdminService } = await import('@/gen/spinneret/v1/site_admin_pb');
  const transport = createRouterTransport(({ service }) => {
    service(SiteAdminService, {
      listSites: () =>
        create(ListSitesResponseSchema, {
          total: 2,
          sites: [
            create(SiteSchema, {
              id: 'sit_1',
              name: 'shop',
              displayName: 'Shop',
              clients: ['web', 'app'],
              endpointGroupCount: 4,
            }),
            create(SiteSchema, {
              id: 'sit_2',
              name: 'market',
              clients: ['web'],
              paused: true,
              pausedReason: 'site redesign',
              pausedAt: timestampFromMs(Date.now() - 60_000),
            }),
          ],
        }),
    });
    service(BreakerAdminService, {
      setSitePaused: (req) => {
        calls.setSitePaused.push(req);
        return create(SetSitePausedResponseSchema, {
          site: req.site,
          paused: req.paused,
          pausedReason: req.reason,
        });
      },
    });
  });
  return {
    siteClient: createClient(SiteAdminService, transport),
    breakerClient: createClient(BreakerAdminService, transport),
  };
});

vi.mock('@/app/auth/AuthContext', async () => {
  const { keepPreviousInScope, scopedKey } = await import('@/lib/queryKeys');
  const auth = {
    tenantId: 'ten_1',
    namespaceName: 'default',
    can: () => calls.canOperate,
    canInTenant: () => calls.canOperate,
  };
  return {
    useAuth: () => auth,
    useScopedQueryKey:
      () =>
      (domain: string, ...parts: unknown[]) =>
        scopedKey(domain as never, 'ten_1', 'default', ...parts),
    useScopedPlaceholder: () => keepPreviousInScope('ten_1', 'default'),
  };
});

const { SiteSwitches } = await import('./SiteSwitches');
const { renderWithProviders } = await import('../testing');

describe('SiteSwitches', () => {
  beforeEach(() => {
    calls.setSitePaused.length = 0;
    calls.canOperate = true;
  });

  it('lists sites with their switch state and pause reason', async () => {
    await renderWithProviders(<SiteSwitches paused />);
    expect(await screen.findByText('Shop')).toBeInTheDocument();
    expect(screen.getByText('site redesign')).toBeInTheDocument();
    expect(screen.getByRole('switch', { name: 'Pause site shop' })).toBeChecked();
    expect(screen.getByRole('switch', { name: 'Resume site market' })).not.toBeChecked();
  });

  it('requires a reason before pausing and sends SetSitePaused', async () => {
    const user = userEvent.setup();
    await renderWithProviders(<SiteSwitches paused />);
    await user.click(await screen.findByRole('switch', { name: 'Pause site shop' }));

    const dialog = await screen.findByRole('alertdialog');
    const confirm = within(dialog).getByRole('button', { name: 'Pause site' });
    expect(confirm).toBeDisabled();
    await user.type(within(dialog).getByRole('textbox'), 'target API changed');
    expect(confirm).toBeEnabled();
    await user.click(confirm);

    await waitFor(() => expect(calls.setSitePaused).toHaveLength(1));
    expect(calls.setSitePaused[0]).toMatchObject({
      namespace: 'default',
      site: 'shop',
      paused: true,
      reason: 'target API changed',
    });
  });

  it('disables switches without breaker:operate', async () => {
    calls.canOperate = false;
    await renderWithProviders(<SiteSwitches paused />);
    expect(await screen.findByRole('switch', { name: 'Pause site shop' })).toBeDisabled();
  });
});
