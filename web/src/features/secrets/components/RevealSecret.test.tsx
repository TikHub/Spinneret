import { create } from '@bufbuild/protobuf';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { TooltipProvider } from '@/components/ui/tooltip';
import {
  SecretInfoSchema,
  type RevealSecretRequest,
  type SecretInfo,
} from '@/gen/spinneret/v1/secret_admin_pb';

const reveals = vi.hoisted(() => [] as RevealSecretRequest[]);

vi.mock('@/lib/clients', async () => {
  const { createClient, createRouterTransport } = await import('@connectrpc/connect');
  const { SecretAdminService } = await import('@/gen/spinneret/v1/secret_admin_pb');
  const transport = createRouterTransport(({ service }) => {
    service(SecretAdminService, {
      listSecretVersions: () => ({ versions: [{ version: 2 }, { version: 1 }] }),
      revealSecret: (req) => {
        reveals.push(req);
        return { value: 'sk-live-0123456789', version: req.version || 2 };
      },
    });
  });
  return { secretAdminClient: createClient(SecretAdminService, transport) };
});

vi.mock('@/app/auth/AuthContext', () => ({
  useAuth: () => ({ namespaceName: 'prod', tenantId: 'ten_1' }),
  useScopedQueryKey:
    () =>
    (domain: string, ...parts: unknown[]) => [domain, 'ten_1', 'prod', ...parts],
  useScopedPlaceholder: () => undefined,
}));

const { RevealSecretDialog, REVEAL_CONFIRM_TEXT } = await import('./RevealSecretDialog');
const { RevealedSecretDialog } = await import('./RevealedSecretDialog');
const { useRevealTimer } = await import('../useRevealTimer');

const secret = create(SecretInfoSchema, { id: 'sec_1', path: 'signing/api_key', currentVersion: 2 });
const VISIBLE_MS = 300;

function Harness() {
  const [target, setTarget] = useState<SecretInfo | undefined>(secret);
  const timer = useRevealTimer(VISIBLE_MS);
  return (
    <>
      <RevealSecretDialog
        secret={target}
        onOpenChange={(open) => !open && setTarget(undefined)}
        onRevealed={timer.show}
      />
      <RevealedSecretDialog path={secret.path} timer={timer} />
    </>
  );
}

describe('secret reveal flow', () => {
  afterEach(() => {
    reveals.length = 0;
  });

  it('requires typed confirmation, reveals the current version and hides it after the timeout', async () => {
    const user = userEvent.setup();
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <TooltipProvider>
          <Harness />
        </TooltipProvider>
      </QueryClientProvider>,
    );

    const confirm = screen.getByRole('button', { name: 'reveal.confirm' });
    expect(confirm).toBeDisabled();
    expect(screen.getByText('reveal.audit')).toBeInTheDocument();

    await user.type(screen.getByRole('textbox'), REVEAL_CONFIRM_TEXT);
    expect(confirm).toBeEnabled();
    await user.click(confirm);

    expect(await screen.findByText('sk-live-0123456789')).toBeInTheDocument();
    expect(reveals).toEqual([expect.objectContaining({ id: 'sec_1', version: 0 })]);

    await waitFor(() => expect(screen.queryByText('sk-live-0123456789')).not.toBeInTheDocument(), {
      timeout: VISIBLE_MS * 5,
    });
  });
});
