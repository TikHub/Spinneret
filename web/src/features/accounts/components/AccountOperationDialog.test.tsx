import { create } from '@bufbuild/protobuf';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import i18next from 'i18next';
import { I18nextProvider, initReactI18next } from 'react-i18next';
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';

import { TooltipProvider } from '@/components/ui/tooltip';
import {
  AccountSchema,
  type OperateAccountRequest,
  type OperateAccountResponse,
} from '@/gen/spinneret/v1/identity_admin_pb';
import enAccounts from '@/i18n/locales/en/accounts.json';
import enCommon from '@/i18n/locales/en/common.json';
import enIdentities from '@/i18n/locales/en/identities.json';

const handlers = vi.hoisted(() => ({ operateAccount: vi.fn() }));

vi.mock('@/lib/clients', async () => {
  const { createClient, createRouterTransport } = await import('@connectrpc/connect');
  const { IdentityAdminService } = await import('@/gen/spinneret/v1/identity_admin_pb');
  const transport = createRouterTransport(({ service }) => {
    service(IdentityAdminService, {
      operateAccount: (req: OperateAccountRequest) => handlers.operateAccount(req),
    });
  });
  return { identityClient: createClient(IdentityAdminService, transport) };
});

const { AccountOperationDialog } = await import('./AccountOperationDialog');

beforeAll(async () => {
  await i18next.use(initReactI18next).init({
    lng: 'en',
    resources: { en: { common: enCommon, identities: enIdentities, accounts: enAccounts } },
    defaultNS: 'common',
    fallbackNS: 'common',
    interpolation: { escapeValue: false },
    react: { useSuspense: false },
  });
});

afterEach(() => {
  handlers.operateAccount.mockReset();
});

const account = create(AccountSchema, {
  id: 'acc_1',
  site: 'shop',
  externalRef: 'user-42',
  state: 'active',
  identityCount: 3,
});

function renderDialog(operation: 'cooldown' | 'enable', onApplied = vi.fn()) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <I18nextProvider i18n={i18next}>
      <QueryClientProvider client={client}>
        <TooltipProvider>
          <AccountOperationDialog
            account={account}
            operation={operation}
            open
            onOpenChange={() => undefined}
            onApplied={onApplied}
          />
        </TooltipProvider>
      </QueryClientProvider>
    </I18nextProvider>,
  );
  return onApplied;
}

describe('AccountOperationDialog', () => {
  it('sends a cooldown with the chosen duration and reports member identities', async () => {
    handlers.operateAccount.mockResolvedValue({
      account: { ...account, cooldownUntil: undefined },
      identities: { matched: 3, succeeded: 3 },
    });
    const onApplied = renderDialog('cooldown');
    expect(screen.getByText('Cooldown account user-42')).toBeInTheDocument();
    expect(screen.getByText('This affects 3 items.')).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText(/Duration/), { target: { value: '2h' } });
    fireEvent.change(screen.getByLabelText(/Reason/), { target: { value: 'rate limited' } });
    fireEvent.click(screen.getByRole('button', { name: 'Cooldown' }));

    await waitFor(() => expect(onApplied).toHaveBeenCalledTimes(1));
    const request = handlers.operateAccount.mock.calls[0]?.[0] as OperateAccountRequest;
    expect(request).toMatchObject({
      id: 'acc_1',
      operation: 'cooldown',
      duration: '2h',
      reason: 'rate limited',
    });
    const response = onApplied.mock.calls[0]?.[0] as OperateAccountResponse;
    expect(response.identities?.succeeded).toBe(3);
  });

  it('rejects a permanent cooldown', () => {
    renderDialog('cooldown');
    fireEvent.change(screen.getByLabelText(/Duration/), { target: { value: 'permanent' } });
    expect(screen.getByRole('button', { name: 'Cooldown' })).toBeDisabled();
  });

  it('enables without a duration', async () => {
    handlers.operateAccount.mockResolvedValue({ account, identities: { matched: 3, succeeded: 3 } });
    const onApplied = renderDialog('enable');
    expect(screen.queryByLabelText(/Duration/)).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Enable' }));
    await waitFor(() => expect(onApplied).toHaveBeenCalled());
    expect((handlers.operateAccount.mock.calls[0]?.[0] as OperateAccountRequest).duration).toBe('');
  });
});
