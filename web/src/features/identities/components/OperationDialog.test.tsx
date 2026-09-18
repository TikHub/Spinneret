import { create } from '@bufbuild/protobuf';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import i18next from 'i18next';
import { I18nextProvider, initReactI18next } from 'react-i18next';
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';

import { TooltipProvider } from '@/components/ui/tooltip';
import { BulkFailureSchema, BulkResultSchema } from '@/gen/spinneret/v1/common_pb';
import { type OperateIdentitiesRequest } from '@/gen/spinneret/v1/identity_admin_pb';
import enCommon from '@/i18n/locales/en/common.json';
import enIdentities from '@/i18n/locales/en/identities.json';

const handlers = vi.hoisted(() => ({ operateIdentities: vi.fn() }));

vi.mock('@/app/auth/AuthContext', () => ({
  useAuth: () => ({ namespaceName: 'default', tenantId: 'ten_1', can: () => true }),
  useScopedQueryKey:
    () =>
    (domain: string, ...parts: unknown[]) => [domain, 'ten_1', 'default', ...parts],
  useScopedPlaceholder: () => undefined,
}));

// IdentityAdminService and SiteAdminService served by an in-memory Connect router.
vi.mock('@/lib/clients', async () => {
  const { createClient, createRouterTransport } = await import('@connectrpc/connect');
  const { IdentityAdminService } = await import('@/gen/spinneret/v1/identity_admin_pb');
  const { SiteAdminService } = await import('@/gen/spinneret/v1/site_admin_pb');
  const transport = createRouterTransport(({ service }) => {
    service(IdentityAdminService, {
      operateIdentities: (req: OperateIdentitiesRequest) => handlers.operateIdentities(req),
    });
    service(SiteAdminService, {
      listEndpointGroups: () => ({
        endpointGroups: [{ id: 'eg_1', name: 'search', client: 'web', site: 'shop' }],
        total: 1,
      }),
      listSites: () => ({ sites: [{ id: 'sit_1', name: 'shop' }], total: 1 }),
    });
  });
  return {
    identityClient: createClient(IdentityAdminService, transport),
    siteClient: createClient(SiteAdminService, transport),
  };
});

const { OperationDialog } = await import('./OperationDialog');

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
  handlers.operateIdentities.mockReset();
});

function renderDialog(operation: 'ban' | 'unban', onApplied = vi.fn()) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <I18nextProvider i18n={i18next}>
      <QueryClientProvider client={client}>
        <TooltipProvider>
          <OperationDialog
            open
            onOpenChange={() => undefined}
            operation={operation}
            ids={['idt_1', 'idt_2', 'idt_1']}
            site="shop"
            onApplied={onApplied}
          />
        </TooltipProvider>
      </QueryClientProvider>
    </I18nextProvider>,
  );
  return onApplied;
}

describe('OperationDialog', () => {
  it('requires typed confirmation for a permanent ban and sends a normalized request', async () => {
    const result = create(BulkResultSchema, {
      matched: 2,
      succeeded: 1,
      failed: [create(BulkFailureSchema, { id: 'idt_2', reason: 'not_found', message: 'gone' })],
    });
    handlers.operateIdentities.mockResolvedValue({ result });
    const onApplied = renderDialog('ban');

    expect(screen.getByText('Ban 3 identities')).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText(/Duration/), { target: { value: 'permanent' } });
    fireEvent.change(screen.getByLabelText(/Reason/), { target: { value: '  leaked cookies ' } });

    const confirm = screen.getByRole('button', { name: 'Ban' });
    expect(confirm).toBeDisabled();
    fireEvent.change(screen.getByLabelText('Type ban to confirm'), { target: { value: 'ban' } });
    expect(confirm).toBeEnabled();
    fireEvent.click(confirm);

    await waitFor(() => expect(onApplied).toHaveBeenCalledTimes(1));
    const request = handlers.operateIdentities.mock.calls[0]?.[0] as OperateIdentitiesRequest;
    expect(request.ids).toEqual(['idt_1', 'idt_2']);
    expect(request.operation).toBe('ban');
    expect(request.duration).toBe('permanent');
    expect(request.reason).toBe('leaked cookies');
    expect(request.scope).toBe('');
    expect(onApplied.mock.calls[0]?.[0]).toMatchObject({ matched: 2, succeeded: 1 });
  });

  it('blocks an invalid duration', () => {
    renderDialog('ban');
    fireEvent.change(screen.getByLabelText(/Duration/), { target: { value: '10 minutes' } });
    expect(screen.getByRole('button', { name: 'Ban' })).toBeDisabled();
  });

  it('sends reset flags for unban without typed confirmation', async () => {
    handlers.operateIdentities.mockResolvedValue({ result: { matched: 2, succeeded: 2 } });
    const onApplied = renderDialog('unban');
    fireEvent.click(screen.getByLabelText('Also reset failure streaks'));
    fireEvent.click(screen.getByRole('button', { name: 'Unban' }));
    await waitFor(() => expect(onApplied).toHaveBeenCalled());
    const request = handlers.operateIdentities.mock.calls[0]?.[0] as OperateIdentitiesRequest;
    expect(request.resetFailures).toBe(true);
    expect(request.resetHealth).toBe(false);
    expect(request.duration).toBe('');
  });
});
