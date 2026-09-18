import { create } from '@bufbuild/protobuf';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import i18next from 'i18next';
import { I18nextProvider, initReactI18next } from 'react-i18next';
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';

import { type SetSitePausedRequest } from '@/gen/spinneret/v1/breaker_admin_pb';
import { SiteSchema } from '@/gen/spinneret/v1/site_admin_pb';
import enCommon from '@/i18n/locales/en/common.json';
import enSites from '@/i18n/locales/en/sites.json';

const calls = vi.hoisted(() => ({ setSitePaused: [] as SetSitePausedRequest[] }));

vi.mock('@/app/auth/AuthContext', () => ({
  useAuth: () => ({
    namespaceName: 'default',
    tenantId: 'ten_1',
    isPlatformAdmin: true,
    can: () => true,
    canInTenant: () => true,
  }),
  useScopedQueryKey:
    () =>
    (domain: string, ...parts: unknown[]) => [domain, 'ten_1', 'default', ...parts],
  useScopedPlaceholder: () => undefined,
}));

vi.mock('@/lib/clients', async () => {
  const { createClient, createRouterTransport } = await import('@connectrpc/connect');
  const { BreakerAdminService, SetSitePausedResponseSchema } =
    await import('@/gen/spinneret/v1/breaker_admin_pb');
  const { create: createMessage } = await import('@bufbuild/protobuf');
  const transport = createRouterTransport(({ service }) => {
    service(BreakerAdminService, {
      setSitePaused: (req) => {
        calls.setSitePaused.push(req);
        return createMessage(SetSitePausedResponseSchema, { site: req.site, paused: req.paused });
      },
    });
  });
  return { breakerClient: createClient(BreakerAdminService, transport), siteClient: {} };
});

const { SitePauseControl } = await import('./SitePauseControl');

const runningSite = create(SiteSchema, { id: 'sit_1', name: 'shop', clients: ['web'] });
const pausedSite = create(SiteSchema, {
  id: 'sit_1',
  name: 'shop',
  clients: ['web'],
  paused: true,
  pausedReason: 'maintenance',
});

beforeAll(async () => {
  await i18next.use(initReactI18next).init({
    lng: 'en',
    resources: { en: { common: enCommon, sites: enSites } },
    defaultNS: 'common',
    fallbackNS: 'common',
    interpolation: { escapeValue: false },
    react: { useSuspense: false },
  });
});

beforeEach(() => {
  calls.setSitePaused.length = 0;
});

function renderControl(site: typeof runningSite) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <I18nextProvider i18n={i18next}>
      <QueryClientProvider client={client}>
        <SitePauseControl site={site} />
      </QueryClientProvider>
    </I18nextProvider>,
  );
}

describe('SitePauseControl', () => {
  it('is on while the site runs and its label names the action it triggers', () => {
    renderControl(runningSite);
    const control = screen.getByRole('switch', { name: 'Pause site shop' });
    expect(control).toBeChecked();
    expect(screen.getByText('Active')).toBeInTheDocument();
  });

  it('is off while the site is paused and offers to resume it', () => {
    renderControl(pausedSite);
    const control = screen.getByRole('switch', { name: 'Resume site shop' });
    expect(control).not.toBeChecked();
    expect(screen.getByText('Paused')).toBeInTheDocument();
  });

  it('turning the switch off pauses the site with the given reason', async () => {
    renderControl(runningSite);
    fireEvent.click(screen.getByRole('switch', { name: 'Pause site shop' }));

    expect(screen.getByRole('alertdialog')).toHaveTextContent('Pause site shop?');
    fireEvent.change(screen.getByRole('textbox', { name: 'Reason' }), {
      target: { value: 'maintenance window' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Pause site' }));

    await waitFor(() => expect(calls.setSitePaused).toHaveLength(1));
    expect(calls.setSitePaused[0]).toMatchObject({
      site: 'shop',
      paused: true,
      reason: 'maintenance window',
    });
  });

  it('turning the switch on resumes the site', async () => {
    renderControl(pausedSite);
    fireEvent.click(screen.getByRole('switch', { name: 'Resume site shop' }));

    expect(screen.getByRole('alertdialog')).toHaveTextContent('Resume site shop?');
    fireEvent.click(screen.getByRole('button', { name: 'Resume site' }));

    await waitFor(() => expect(calls.setSitePaused).toHaveLength(1));
    expect(calls.setSitePaused[0]).toMatchObject({ site: 'shop', paused: false, reason: '' });
  });
});
