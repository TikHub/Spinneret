import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { TooltipProvider } from '@/components/ui/tooltip';
import { type CreateSecretRequest } from '@/gen/spinneret/v1/secret_admin_pb';

const creates = vi.hoisted(() => [] as CreateSecretRequest[]);

vi.mock('@/lib/clients', async () => {
  const { createClient, createRouterTransport } = await import('@connectrpc/connect');
  const { SecretAdminService } = await import('@/gen/spinneret/v1/secret_admin_pb');
  const transport = createRouterTransport(({ service }) => {
    service(SecretAdminService, {
      createSecret: (req) => {
        creates.push(req);
        return { secret: { id: 'sec_new', path: req.path, currentVersion: 1 } };
      },
    });
  });
  return { secretAdminClient: createClient(SecretAdminService, transport) };
});

vi.mock('@/app/auth/AuthContext', () => ({
  useAuth: () => ({ namespaceName: 'prod', tenantId: 'ten_1' }),
}));

const { SecretFormDialog } = await import('./SecretFormDialog');

const PLAINTEXT = 'sk-live-plaintext-value';

function Harness() {
  const [open, setOpen] = useState(true);
  return (
    <>
      <span data-testid="state">{open ? 'open' : 'closed'}</span>
      <SecretFormDialog open={open} onOpenChange={setOpen} pathPrefix="signing/" />
    </>
  );
}

describe('SecretFormDialog', () => {
  afterEach(() => {
    creates.length = 0;
  });

  it('validates the path, creates the secret and drops the plaintext from the mutation cache on close', async () => {
    const user = userEvent.setup();
    const client = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <TooltipProvider>
          <Harness />
        </TooltipProvider>
      </QueryClientProvider>,
    );

    const path = screen.getByLabelText(/fields\.path/);
    expect(path).toHaveValue('signing/');
    await user.type(screen.getByLabelText(/fields\.value/), PLAINTEXT);
    await user.click(screen.getByRole('button', { name: 'common:actions.create' }));
    // "signing/" ends with an empty segment.
    expect(await screen.findByText('validation.path.segments')).toBeInTheDocument();
    expect(creates).toHaveLength(0);

    await user.type(path, 'api_key');
    await user.click(screen.getByRole('button', { name: 'common:actions.create' }));

    await waitFor(() => expect(screen.getByTestId('state')).toHaveTextContent('closed'));
    expect(creates).toEqual([
      expect.objectContaining({ namespace: 'prod', path: 'signing/api_key', value: PLAINTEXT }),
    ]);
    await waitFor(() => expect(client.getMutationCache().getAll()).toHaveLength(0));
  });
});
