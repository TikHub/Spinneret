import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import i18next from 'i18next';
import { useState } from 'react';
import { I18nextProvider, initReactI18next } from 'react-i18next';
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';

import { TooltipProvider } from '@/components/ui/tooltip';
import { type ImportIdentitiesRequest } from '@/gen/spinneret/v1/identity_admin_pb';
import enCommon from '@/i18n/locales/en/common.json';
import enIdentities from '@/i18n/locales/en/identities.json';

const calls = vi.hoisted(() => ({
  importIdentities: [] as ImportIdentitiesRequest[],
  /** Response the mocked server returns; a test may replace it. */
  response: { created: 1 } as Record<string, unknown>,
  toasts: [] as { kind: string; text: string }[],
}));

vi.mock('sonner', () => ({
  toast: {
    success: (text: string) => calls.toasts.push({ kind: 'success', text }),
    error: (text: string) => calls.toasts.push({ kind: 'error', text }),
    info: (text: string) => calls.toasts.push({ kind: 'info', text }),
    warning: (text: string) => calls.toasts.push({ kind: 'warning', text }),
  },
}));

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
        return calls.response;
      },
      listIdentityTypes: () => ({
        identityTypes: [
          {
            id: 'ityp_1',
            name: 'cookie',
            site: 'douyin_web',
            client: 'web',
            fields: [
              { name: 'cookies', type: 'cookie_map', required: true, sensitive: true },
              { name: 'user_id', type: 'string', required: true },
              { name: 'user_agent', type: 'string', required: true },
              { name: 'verified', type: 'bool' },
            ],
          },
        ],
      }),
    });
    service(SiteAdminService, {
      listSites: () => ({ sites: [{ id: 'sit_1', name: 'douyin_web' }], total: 1 }),
    });
  });
  return {
    identityClient: createClient(IdentityAdminService, transport),
    siteClient: createClient(SiteAdminService, transport),
  };
});

const { NewIdentityDialog } = await import('./NewIdentityDialog');

const COOKIE = 'ttwid=1%7Cabc; sessionid=deadbeef';
const UA = 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:153.0) Gecko/20100101 Firefox/153.0';

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

beforeEach(() => {
  calls.importIdentities.length = 0;
  calls.toasts.length = 0;
  calls.response = { created: 1 };
});

/** Mounts the dialog only while open, like the identities page. */
function Harness() {
  const [open, setOpen] = useState(true);
  return open ? (
    <NewIdentityDialog open onOpenChange={setOpen} defaultSite="douyin_web" defaultType="cookie" />
  ) : (
    <p>closed</p>
  );
}

function renderDialog() {
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
  return client;
}

/** Waits for the form the default type preloads. */
function fieldsLoaded() {
  return screen.findByRole('textbox', { name: /cookies/ });
}

describe('NewIdentityDialog', () => {
  it('renders one control per declared field, typed by the field type', async () => {
    renderDialog();
    await fieldsLoaded();

    // cookie_map is pasted, so it gets a textarea; the scalars get inputs.
    expect(await screen.findByRole('textbox', { name: /cookies/ })).toBeInstanceOf(HTMLTextAreaElement);
    expect(screen.getByRole('textbox', { name: /user_id/ })).toBeInstanceOf(HTMLInputElement);
    expect(screen.getByRole('switch', { name: /verified/ })).toBeInTheDocument();
  });

  it('sends one JSON Lines row built from the form', async () => {
    const user = userEvent.setup();
    renderDialog();
    await fieldsLoaded();

    fireEvent.change(await screen.findByRole('textbox', { name: /cookies/ }), { target: { value: COOKIE } });
    fireEvent.change(screen.getByRole('textbox', { name: /user_id/ }), { target: { value: '95741160741' } });
    fireEvent.change(screen.getByRole('textbox', { name: /user_agent/ }), { target: { value: UA } });
    fireEvent.change(screen.getByRole('textbox', { name: 'Account' }), {
      target: { value: 'douyin_web_user_id_95741160741' },
    });
    await user.click(screen.getByRole('button', { name: 'Create identity' }));

    await waitFor(() => expect(calls.importIdentities).toHaveLength(1));
    const req = calls.importIdentities[0];
    expect(req).toMatchObject({ site: 'douyin_web', type: 'cookie', format: 'jsonl', mode: 'upsert' });
    expect(req?.data.includes('\n')).toBe(false);
    expect(JSON.parse(req?.data ?? '{}')).toEqual({
      cookies: COOKIE,
      user_id: '95741160741',
      user_agent: UA,
      verified: false,
      _account: 'douyin_web_user_id_95741160741',
    });
  });

  it('marks the missing required fields and sends nothing', async () => {
    const user = userEvent.setup();
    renderDialog();
    await fieldsLoaded();

    fireEvent.change(await screen.findByRole('textbox', { name: /cookies/ }), { target: { value: COOKIE } });
    await user.click(screen.getByRole('button', { name: 'Create identity' }));

    expect(await screen.findAllByText('Required.')).toHaveLength(2);
    expect(calls.importIdentities).toHaveLength(0);
  });

  it('shows why the server rejected the row and keeps the form open', async () => {
    const user = userEvent.setup();
    calls.response = { created: 0, failed: [{ line: 1, message: 'unique_by path "user_id" is missing' }] };
    renderDialog();
    await fieldsLoaded();

    fireEvent.change(await screen.findByRole('textbox', { name: /cookies/ }), { target: { value: COOKIE } });
    fireEvent.change(screen.getByRole('textbox', { name: /user_id/ }), { target: { value: '1' } });
    fireEvent.change(screen.getByRole('textbox', { name: /user_agent/ }), { target: { value: UA } });
    await user.click(screen.getByRole('button', { name: 'Create identity' }));

    await waitFor(() =>
      expect(calls.toasts).toEqual([
        { kind: 'error', text: 'Rejected: unique_by path "user_id" is missing' },
      ]),
    );
    expect(screen.queryByText('closed')).not.toBeInTheDocument();
  });

  it('drops the submitted credentials from the mutation cache once the dialog closes', async () => {
    const user = userEvent.setup();
    const client = renderDialog();
    await fieldsLoaded();

    fireEvent.change(await screen.findByRole('textbox', { name: /cookies/ }), { target: { value: COOKIE } });
    fireEvent.change(screen.getByRole('textbox', { name: /user_id/ }), { target: { value: '1' } });
    fireEvent.change(screen.getByRole('textbox', { name: /user_agent/ }), { target: { value: UA } });
    await user.click(screen.getByRole('button', { name: 'Create identity' }));

    expect(await screen.findByText('closed')).toBeInTheDocument();
    await waitFor(() => expect(client.getMutationCache().getAll()).toHaveLength(0));
  });
});
