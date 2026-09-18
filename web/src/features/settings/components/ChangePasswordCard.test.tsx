import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';

import '@/i18n';

import { type ChangePasswordRequest } from '@/gen/spinneret/v1/auth_pb';

const calls = vi.hoisted(() => ({ changePassword: [] as ChangePasswordRequest[] }));

vi.mock('@/lib/clients', async () => {
  const { createClient, createRouterTransport } = await import('@connectrpc/connect');
  const { AuthService } = await import('@/gen/spinneret/v1/auth_pb');
  const transport = createRouterTransport(({ service }) => {
    service(AuthService, {
      changePassword: (req) => {
        calls.changePassword.push(req);
        return {};
      },
    });
  });
  return { authClient: createClient(AuthService, transport) };
});

const { ChangePasswordCard } = await import('./ChangePasswordCard');

describe('ChangePasswordCard', () => {
  it('changes the password without keeping either password in the mutation cache', async () => {
    const user = userEvent.setup();
    const client = new QueryClient();
    render(
      <QueryClientProvider client={client}>
        <ChangePasswordCard />
      </QueryClientProvider>,
    );

    await user.type(screen.getByLabelText(/^Current password/), 'old-password-1');
    await user.type(screen.getByLabelText(/^New password/), 'new-password-22');
    await user.type(screen.getByLabelText(/^Confirm new password/), 'new-password-22');
    await user.click(screen.getByRole('button', { name: 'Change password' }));

    await waitFor(() =>
      expect(calls.changePassword).toEqual([
        expect.objectContaining({ currentPassword: 'old-password-1', newPassword: 'new-password-22' }),
      ]),
    );
    await waitFor(() => expect(screen.getByLabelText(/^Current password/)).toHaveValue(''));
    // The card stays mounted on the profile page: the finished mutation must not keep the passwords.
    await waitFor(() => expect(client.getMutationCache().getAll()).toHaveLength(0));
  });
});
