import { create } from '@bufbuild/protobuf';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState, type ReactNode } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { TooltipProvider } from '@/components/ui/tooltip';

import {
  ChannelSchema,
  type CreateChannelRequest,
  type UpdateChannelRequest,
} from '@/gen/spinneret/v1/notification_admin_pb';

const calls = vi.hoisted(() => ({
  update: [] as UpdateChannelRequest[],
  create: [] as CreateChannelRequest[],
}));

vi.mock('@/lib/clients', async () => {
  const { createClient, createRouterTransport } = await import('@connectrpc/connect');
  const { NotificationAdminService } = await import('@/gen/spinneret/v1/notification_admin_pb');
  const { SiteAdminService } = await import('@/gen/spinneret/v1/site_admin_pb');
  const transport = createRouterTransport(({ service }) => {
    service(NotificationAdminService, {
      updateChannel: (req) => {
        calls.update.push(req);
        return { channel: { id: req.id, name: req.name } };
      },
      createChannel: (req) => {
        calls.create.push(req);
        return { channel: { id: 'nch_new', name: req.name } };
      },
    });
    service(SiteAdminService, {
      listSites: () => ({ sites: [{ name: 'shop' }, { name: 'market' }] }),
    });
  });
  return {
    notificationClient: createClient(NotificationAdminService, transport),
    siteClient: createClient(SiteAdminService, transport),
  };
});

vi.mock('@/app/auth/AuthContext', () => ({
  useAuth: () => ({
    namespaceName: 'prod',
    tenantId: 'ten_1',
    can: () => true,
    canInTenant: () => true,
  }),
  useScopedQueryKey:
    () =>
    (domain: string, ...parts: unknown[]) => [domain, 'ten_1', 'prod', ...parts],
  useScopedPlaceholder: () => undefined,
}));

const { ChannelFormDialog } = await import('./ChannelFormDialog');

const feishu = create(ChannelSchema, {
  id: 'nch_1',
  namespace: 'prod',
  name: 'ops-feishu',
  kind: 'feishu',
  config: { webhook_url: 'https://open.feishu.cn/••••wxyz', secret: '••••abcd' },
  eventTypes: ['breaker_opened'],
  sites: [],
  minSeverity: 'warning',
  enabled: true,
});

function renderDialog(ui: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const result = render(
    <QueryClientProvider client={client}>
      <TooltipProvider>{ui}</TooltipProvider>
    </QueryClientProvider>,
  );
  return { client, ...result };
}

/** Dialog that closes itself like the channels tab does. */
function ClosingDialog() {
  const [open, setOpen] = useState(true);
  return (
    <>
      <span data-testid="state">{open ? 'open' : 'closed'}</span>
      <ChannelFormDialog open={open} onOpenChange={setOpen} channel={feishu} />
    </>
  );
}

describe('ChannelFormDialog', () => {
  afterEach(() => {
    calls.update.length = 0;
    calls.create.length = 0;
  });

  it('keeps masked settings on the server when only the name changes', async () => {
    const user = userEvent.setup();
    const onOpenChange = vi.fn();
    renderDialog(<ChannelFormDialog open onOpenChange={onOpenChange} channel={feishu} />);

    const name = screen.getByLabelText(/fields\.name/);
    await user.clear(name);
    await user.type(name, 'ops-feishu-2');
    await user.click(screen.getByRole('button', { name: 'common:actions.save' }));

    await waitFor(() => expect(calls.update).toHaveLength(1));
    expect(calls.update[0]?.name).toBe('ops-feishu-2');
    expect(calls.update[0]?.config).toBeUndefined();
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
  });

  it('sends a replaced secret with the untouched masked URL', async () => {
    const user = userEvent.setup();
    renderDialog(<ChannelFormDialog open onOpenChange={vi.fn()} channel={feishu} />);

    const secret = screen.getByLabelText(/config\.secret/);
    expect(secret).toHaveValue('••••abcd');
    expect(secret).toHaveAttribute('readonly');
    // The second "Replace" button belongs to the secret (the first to the webhook URL).
    const replaceButtons = screen.getAllByRole('button', { name: /masked\.replace/ });
    await user.click(replaceButtons[1] as HTMLElement);
    await user.type(screen.getByLabelText(/config\.secret/), 'rotated-secret');
    await user.click(screen.getByRole('button', { name: 'common:actions.save' }));

    await waitFor(() => expect(calls.update).toHaveLength(1));
    expect(calls.update[0]?.config).toEqual({
      webhook_url: 'https://open.feishu.cn/••••wxyz',
      secret: 'rotated-secret',
    });
  });

  it('drops typed credentials from the mutation cache once the dialog closes', async () => {
    const user = userEvent.setup();
    const { client } = renderDialog(<ClosingDialog />);

    const replaceButtons = screen.getAllByRole('button', { name: /masked\.replace/ });
    await user.click(replaceButtons[1] as HTMLElement);
    await user.type(screen.getByLabelText(/config\.secret/), 'rotated-secret');
    await user.click(screen.getByRole('button', { name: 'common:actions.save' }));

    await waitFor(() => expect(screen.getByTestId('state')).toHaveTextContent('closed'));
    expect(calls.update[0]?.config).toMatchObject({ secret: 'rotated-secret' });
    await waitFor(() => expect(client.getMutationCache().getAll()).toHaveLength(0));
  });

  it('validates required settings before creating', async () => {
    const user = userEvent.setup();
    renderDialog(<ChannelFormDialog open onOpenChange={vi.fn()} />);

    await user.type(screen.getByLabelText(/fields\.name/), 'hooks');
    await user.click(screen.getByRole('button', { name: 'common:actions.create' }));
    expect(await screen.findByText('validation.config.required')).toBeInTheDocument();
    expect(calls.create).toHaveLength(0);

    await user.type(screen.getByLabelText(/config\.url/), 'https://hooks.example.com/a');
    await user.click(screen.getByRole('button', { name: 'common:actions.create' }));
    await waitFor(() => expect(calls.create).toHaveLength(1));
    expect(calls.create[0]).toMatchObject({
      namespace: 'prod',
      name: 'hooks',
      kind: 'webhook',
      config: { url: 'https://hooks.example.com/a' },
      enabled: true,
    });
  });
});
