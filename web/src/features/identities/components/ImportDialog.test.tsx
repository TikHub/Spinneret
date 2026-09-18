import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import i18next from 'i18next';
import { useState } from 'react';
import { I18nextProvider, initReactI18next } from 'react-i18next';
import { beforeAll, describe, expect, it, vi } from 'vitest';

import { TooltipProvider } from '@/components/ui/tooltip';
import { type ImportIdentitiesRequest } from '@/gen/spinneret/v1/identity_admin_pb';
import enCommon from '@/i18n/locales/en/common.json';
import enIdentities from '@/i18n/locales/en/identities.json';

const calls = vi.hoisted(() => ({ importIdentities: [] as ImportIdentitiesRequest[] }));

vi.mock('@/app/auth/AuthContext', () => ({
  useAuth: () => ({ namespaceName: 'default', tenantId: 'ten_1', can: () => true }),
  useScopedQueryKey:
    () =>
    (domain: string, ...parts: unknown[]) => [domain, 'ten_1', 'default', ...parts],
  useScopedPlaceholder: () => undefined,
}));

vi.mock('@/lib/clients', async () => {
  const { createClient, createRouterTransport } = await import('@connectrpc/connect');
  const { IdentityAdminService } = await import('@/gen/spinneret/v1/identity_admin_pb');
  const { SiteAdminService } = await import('@/gen/spinneret/v1/site_admin_pb');
  const transport = createRouterTransport(({ service }) => {
    service(IdentityAdminService, {
      importIdentities: (req) => {
        calls.importIdentities.push(req);
        return { created: 1 };
      },
      listIdentityTypes: () => ({ identityTypes: [{ id: 'ityp_1', name: 'cookie', site: 'shop' }] }),
    });
    service(SiteAdminService, {
      listSites: () => ({ sites: [{ id: 'sit_1', name: 'shop' }], total: 1 }),
    });
  });
  return {
    identityClient: createClient(IdentityAdminService, transport),
    siteClient: createClient(SiteAdminService, transport),
  };
});

const { ImportDialog } = await import('./ImportDialog');

const SECRET_ROW = '{"cookies": "sessionid=top-secret-session", "_account": "user-1"}';

beforeAll(async () => {
  await i18next.use(initReactI18next).init({
    lng: 'en',
    resources: { en: { common: enCommon, identities: enIdentities } },
    defaultNS: 'common',
    fallbackNS: 'common',
    interpolation: { escapeValue: false },
    react: { useSuspense: false },
  });
});

/** Mounts the dialog only while open, like the identities page. */
function Harness() {
  const [open, setOpen] = useState(true);
  return open ? (
    <ImportDialog open onOpenChange={setOpen} defaultSite="shop" defaultType="cookie" />
  ) : (
    <p>closed</p>
  );
}

describe('ImportDialog', () => {
  it('drops the imported credentials from the mutation cache once the dialog closes', async () => {
    const user = userEvent.setup();
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <I18nextProvider i18n={i18next}>
        <QueryClientProvider client={client}>
          <TooltipProvider>
            <Harness />
          </TooltipProvider>
        </QueryClientProvider>
      </I18nextProvider>,
    );

    await user.click(screen.getByRole('tab', { name: 'Paste' }));
    fireEvent.change(screen.getByRole('textbox', { name: 'Paste' }), { target: { value: SECRET_ROW } });
    await user.click(screen.getByRole('button', { name: 'Dry run' }));

    await waitFor(() =>
      expect(calls.importIdentities).toEqual([
        expect.objectContaining({ site: 'shop', type: 'cookie', data: SECRET_ROW, dryRun: true }),
      ]),
    );
    await user.click(screen.getAllByRole('button', { name: 'Close' })[0] as HTMLElement);
    expect(await screen.findByText('closed')).toBeInTheDocument();
    await waitFor(() => expect(client.getMutationCache().getAll()).toHaveLength(0));
  });
});
