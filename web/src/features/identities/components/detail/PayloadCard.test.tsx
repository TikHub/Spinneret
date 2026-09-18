import { create, type JsonObject } from '@bufbuild/protobuf';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import i18next from 'i18next';
import { I18nextProvider, initReactI18next } from 'react-i18next';
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';

import { TooltipProvider } from '@/components/ui/tooltip';
import { IdentitySchema, type GetIdentityRequest } from '@/gen/spinneret/v1/identity_admin_pb';
import enCommon from '@/i18n/locales/en/common.json';
import enIdentities from '@/i18n/locales/en/identities.json';

import { useRevealedPayload } from '../../reveal';

const handlers = vi.hoisted(() => ({ getIdentity: vi.fn(), toastError: vi.fn() }));

vi.mock('sonner', () => ({ toast: { error: handlers.toastError, success: vi.fn() } }));

vi.mock('@/app/auth/AuthContext', () => ({
  useAuth: () => ({ namespaceName: 'default', tenantId: 'ten_1', can: () => true }),
}));

vi.mock('@/lib/clients', async () => {
  const { createClient, createRouterTransport } = await import('@connectrpc/connect');
  const { IdentityAdminService } = await import('@/gen/spinneret/v1/identity_admin_pb');
  const transport = createRouterTransport(({ service }) => {
    service(IdentityAdminService, {
      getIdentity: (req: GetIdentityRequest) => handlers.getIdentity(req),
    });
  });
  return { identityClient: createClient(IdentityAdminService, transport) };
});

const { PayloadCard } = await import('./PayloadCard');

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

afterEach(() => {
  handlers.getIdentity.mockReset();
  handlers.toastError.mockReset();
});

const identity = create(IdentitySchema, { id: 'idt_1', site: 'shop', payloadVersion: 2 });
const masked: JsonObject = { cookies: '••••cret', user_agent: 'Mozilla' };

function Harness() {
  const reveal = useRevealedPayload(identity.id);
  return <PayloadCard identity={identity} maskedPayload={masked} reveal={reveal} onEdit={() => undefined} />;
}

function renderCard() {
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
}

async function confirmReveal() {
  fireEvent.click(screen.getByRole('button', { name: 'Reveal' }));
  const dialog = await screen.findByRole('alertdialog');
  const confirm = within(dialog).getByRole('button', { name: 'Reveal' });
  expect(confirm).toBeDisabled();
  fireEvent.change(within(dialog).getByLabelText('Type reveal to confirm'), { target: { value: 'reveal' } });
  fireEvent.click(confirm);
}

describe('PayloadCard reveal', () => {
  it('shows the clear-text payload after typed confirmation', async () => {
    handlers.getIdentity.mockResolvedValue({
      identity,
      payload: { cookies: 'sessionid=secret', user_agent: 'Mozilla' },
      revealed: true,
    });
    renderCard();
    expect(screen.getByText('1 field masked')).toBeInTheDocument();

    await confirmReveal();

    expect(await screen.findByText('"sessionid=secret"')).toBeInTheDocument();
    expect(screen.getByText('Revealed · 60 s')).toBeInTheDocument();
    const request = handlers.getIdentity.mock.calls[0]?.[0] as GetIdentityRequest;
    expect(request).toMatchObject({ id: 'idt_1', reveal: true });
  });

  it('stays masked and reports the refusal when the server does not reveal', async () => {
    handlers.getIdentity.mockResolvedValue({ identity, payload: masked, revealed: false });
    renderCard();

    await confirmReveal();

    await waitFor(() => expect(handlers.toastError).toHaveBeenCalledTimes(1));
    expect(String(handlers.toastError.mock.calls[0]?.[0])).toContain('identity:reveal');
    expect(screen.queryByText(/Revealed ·/)).not.toBeInTheDocument();
    expect(screen.getByText('1 field masked')).toBeInTheDocument();
  });
});
