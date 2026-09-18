import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import i18next from 'i18next';
import { I18nextProvider, initReactI18next } from 'react-i18next';
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';

import { TooltipProvider } from '@/components/ui/tooltip';
import { type OperateProxiesRequest } from '@/gen/spinneret/v1/proxy_admin_pb';
import enCommon from '@/i18n/locales/en/common.json';
import enProxies from '@/i18n/locales/en/proxies.json';

import { type ProxyOperation } from '../proxyOperations';

const handlers = vi.hoisted(() => ({ operateProxies: vi.fn() }));

vi.mock('@/app/auth/AuthContext', () => ({
  useAuth: () => ({ namespaceName: 'default', tenantId: 'ten_1', can: () => true }),
  useScopedQueryKey:
    () =>
    (domain: string, ...parts: unknown[]) => [domain, 'ten_1', 'default', ...parts],
  useScopedPlaceholder: () => undefined,
}));

// Proxy and site services are served by an in-memory Connect router (no backend).
vi.mock('@/lib/clients', async () => {
  const { createClient, createRouterTransport } = await import('@connectrpc/connect');
  const { ProxyAdminService } = await import('@/gen/spinneret/v1/proxy_admin_pb');
  const { SiteAdminService } = await import('@/gen/spinneret/v1/site_admin_pb');
  const transport = createRouterTransport(({ service }) => {
    service(ProxyAdminService, {
      operateProxies: (req: OperateProxiesRequest) => handlers.operateProxies(req),
    });
    service(SiteAdminService, {
      listSites: () => ({ sites: [{ id: 'sit_1', name: 'shop', displayName: 'Shop' }], total: 1 }),
    });
  });
  return {
    proxyClient: createClient(ProxyAdminService, transport),
    siteClient: createClient(SiteAdminService, transport),
  };
});

const { OperateProxiesDialog } = await import('./OperateProxiesDialog');

beforeAll(async () => {
  await i18next.use(initReactI18next).init({
    lng: 'en',
    resources: { en: { common: enCommon, proxies: enProxies } },
    defaultNS: 'common',
    fallbackNS: 'common',
    interpolation: { escapeValue: false },
    react: { useSuspense: false },
  });
});

afterEach(() => {
  handlers.operateProxies.mockReset();
});

function renderDialog(operation: ProxyOperation, onDone = vi.fn()) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <I18nextProvider i18n={i18next}>
      <QueryClientProvider client={client}>
        <TooltipProvider>
          <OperateProxiesDialog
            open
            onOpenChange={() => undefined}
            operation={operation}
            ids={['pxy_1', 'pxy_2', 'pxy_1']}
            onDone={onDone}
          />
        </TooltipProvider>
      </QueryClientProvider>
    </I18nextProvider>,
  );
  return onDone;
}

describe('OperateProxiesDialog', () => {
  it('sends a bulk cooldown with duration and reason and reports the result', async () => {
    handlers.operateProxies.mockReturnValue({ result: { matched: 2, succeeded: 2, failed: [] } });
    const onDone = renderDialog('cooldown');

    const confirm = screen.getByRole('button', { name: 'Cool down' });
    expect(confirm).toBeDisabled();
    fireEvent.change(screen.getByRole('textbox', { name: /Duration/ }), { target: { value: '30m' } });
    fireEvent.change(screen.getByRole('textbox', { name: 'Reason' }), {
      target: { value: ' captcha wave ' },
    });
    await waitFor(() => expect(confirm).toBeEnabled());
    fireEvent.click(confirm);

    await waitFor(() => expect(handlers.operateProxies).toHaveBeenCalledTimes(1));
    const request = handlers.operateProxies.mock.calls[0]?.[0] as OperateProxiesRequest;
    expect(request).toMatchObject({
      ids: ['pxy_1', 'pxy_2'],
      operation: 'cooldown',
      site: '',
      duration: '30m',
      reason: 'captcha wave',
    });
    await waitFor(() => expect(onDone).toHaveBeenCalledTimes(1));
    expect(onDone.mock.calls[0]?.[1]).toEqual(['pxy_1', 'pxy_2']);
  });

  it('requires typing the operation for a permanent ban', async () => {
    handlers.operateProxies.mockReturnValue({ result: { matched: 2, succeeded: 2, failed: [] } });
    renderDialog('ban');

    fireEvent.change(screen.getByRole('textbox', { name: /Duration/ }), { target: { value: 'permanent' } });
    const confirm = screen.getByRole('button', { name: 'Ban' });
    expect(await screen.findByText(/A permanent ban never ends by itself/)).toBeInTheDocument();
    expect(confirm).toBeDisabled();

    fireEvent.change(screen.getByRole('textbox', { name: 'Type ban to confirm' }), {
      target: { value: 'ban' },
    });
    await waitFor(() => expect(confirm).toBeEnabled());
    fireEvent.click(confirm);
    await waitFor(() => expect(handlers.operateProxies).toHaveBeenCalledTimes(1));
    expect((handlers.operateProxies.mock.calls[0]?.[0] as OperateProxiesRequest).duration).toBe('permanent');
  });
});
