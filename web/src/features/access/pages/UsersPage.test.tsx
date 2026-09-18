import { create } from '@bufbuild/protobuf';
import { Code, ConnectError } from '@connectrpc/connect';
import { type MutationCache } from '@tanstack/react-query';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { TenantUserSchema, type CreateUserRequest } from '@/gen/spinneret/v1/access_admin_pb';
import { RoleBindingSchema, UserSchema } from '@/gen/spinneret/v1/auth_pb';

const api = vi.hoisted(() => ({
  getMe: vi.fn(),
  listUsers: vi.fn(),
  createUser: vi.fn(),
  resetPassword: vi.fn(),
  listRoleBindings: vi.fn(),
  deleteRoleBinding: vi.fn(),
  listSites: vi.fn(),
}));

vi.mock('@/lib/clients', async () => {
  const { createClient, createRouterTransport } = await import('@connectrpc/connect');
  const { AuthService } = await import('@/gen/spinneret/v1/auth_pb');
  const { AccessAdminService } = await import('@/gen/spinneret/v1/access_admin_pb');
  const { SiteAdminService } = await import('@/gen/spinneret/v1/site_admin_pb');
  const transport = createRouterTransport((router) => {
    router.service(AuthService, { getMe: () => api.getMe() });
    router.service(AccessAdminService, {
      listUsers: (req) => api.listUsers(req),
      createUser: (req) => api.createUser(req),
      resetPassword: (req) => api.resetPassword(req),
      listRoleBindings: (req) => api.listRoleBindings(req),
      deleteRoleBinding: (req) => api.deleteRoleBinding(req),
    });
    router.service(SiteAdminService, { listSites: (req) => api.listSites(req) });
  });
  return {
    authClient: createClient(AuthService, transport),
    accessClient: createClient(AccessAdminService, transport),
    siteClient: createClient(SiteAdminService, transport),
  };
});

const { meResponse, renderWithProviders } = await import('../testing/providers');

/** Serialized variables of every mutation still held by the React Query cache. */
function cachedMutationVariables(queryClient: { getMutationCache: () => MutationCache }): string {
  return JSON.stringify(
    queryClient
      .getMutationCache()
      .getAll()
      .map((m) => m.state.variables),
  );
}
const { default: UsersPage } = await import('./UsersPage');

const ownerBinding = create(RoleBindingSchema, {
  id: 'rb_owner',
  userId: 'usr_alice',
  username: 'alice',
  tenantId: 'ten_1',
  role: 'owner',
});
const alice = create(TenantUserSchema, {
  user: create(UserSchema, { id: 'usr_alice', username: 'alice', displayName: 'Alice', email: 'a@x.io' }),
  bindings: [ownerBinding],
});

describe('UsersPage', () => {
  beforeEach(() => {
    window.localStorage.clear();
    api.getMe.mockResolvedValue(meResponse({ role: 'owner', permissions: ['user:read', 'user:write'] }));
    api.listUsers.mockResolvedValue({ users: [alice], total: 1 });
    api.listSites.mockResolvedValue({ sites: [{ name: 'shop' }, { name: 'market' }] });
    api.listRoleBindings.mockResolvedValue({ bindings: [ownerBinding], total: 1 });
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it('lists members with their binding summary', async () => {
    renderWithProviders(<UsersPage />);
    expect(await screen.findByText('alice')).toBeInTheDocument();
    expect(screen.getByText('Owner')).toBeInTheDocument();
    expect(screen.getByText('All namespaces')).toBeInTheDocument();
  });

  it('creates a user whose binding is narrowed to sites of one namespace', async () => {
    const user = userEvent.setup();
    api.createUser.mockResolvedValue({ user: { id: 'usr_bob', username: 'bob' } });
    const { queryClient } = renderWithProviders(<UsersPage />);

    await user.click(await screen.findByRole('button', { name: 'Create user' }));
    const dialog = await screen.findByRole('dialog', { name: 'Create user' });
    await user.type(within(dialog).getByLabelText(/^Username/), 'bob');
    await user.type(within(dialog).getByLabelText(/^Initial password/), 'short');

    // Sites are only offered once a namespace is chosen.
    expect(within(dialog).queryByLabelText('Sites')).not.toBeInTheDocument();
    await user.click(within(dialog).getByRole('combobox', { name: 'Namespace' }));
    await user.click(await screen.findByRole('option', { name: 'prod' }));
    await user.click(within(dialog).getByLabelText('Sites'));
    await user.click(await screen.findByLabelText('shop'));
    await user.keyboard('{Escape}');

    await user.click(within(dialog).getByRole('button', { name: 'Create user' }));
    expect(await within(dialog).findByText('At least 10 characters.')).toBeInTheDocument();
    expect(api.createUser).not.toHaveBeenCalled();

    await user.type(within(dialog).getByLabelText(/^Initial password/), '-and-longer');
    await user.click(within(dialog).getByRole('button', { name: 'Create user' }));
    await waitFor(() => expect(api.createUser).toHaveBeenCalledOnce());
    expect(api.createUser.mock.calls[0]?.[0] as CreateUserRequest).toMatchObject({
      username: 'bob',
      password: 'short-and-longer',
      role: 'viewer',
      namespace: 'prod',
      sites: ['shop'],
      extraPermissions: [],
    });
    // The initial password must not outlive the dialog in the mutation cache.
    await waitFor(() =>
      expect(screen.queryByRole('dialog', { name: 'Create user' })).not.toBeInTheDocument(),
    );
    await waitFor(() => expect(cachedMutationVariables(queryClient)).not.toContain('short-and-longer'));
  });

  it('resets a password and drops it from the mutation cache once the dialog closes', async () => {
    const user = userEvent.setup();
    api.resetPassword.mockResolvedValue({});
    const { queryClient } = renderWithProviders(<UsersPage />);

    await user.click(await screen.findByRole('button', { name: 'Reset password' }));
    const dialog = await screen.findByRole('dialog', { name: 'Reset password of alice' });
    await user.type(within(dialog).getByLabelText(/^New password/), 'correct-horse-battery');
    await user.type(within(dialog).getByLabelText(/^Confirm new password/), 'correct-horse-battery');
    await user.click(within(dialog).getByRole('button', { name: 'Reset password' }));

    await waitFor(() =>
      expect(api.resetPassword).toHaveBeenCalledWith(
        expect.objectContaining({ userId: 'usr_alice', newPassword: 'correct-horse-battery' }),
      ),
    );
    await waitFor(() =>
      expect(screen.queryByRole('dialog', { name: 'Reset password of alice' })).not.toBeInTheDocument(),
    );
    await waitFor(() => expect(queryClient.getMutationCache().getAll()).toHaveLength(0));
  });

  it('shows the server reason when the last owner binding cannot be deleted', async () => {
    const user = userEvent.setup();
    api.deleteRoleBinding.mockRejectedValue(
      new ConnectError('cannot remove the last owner of the tenant', Code.FailedPrecondition),
    );
    renderWithProviders(<UsersPage />);

    await user.click(await screen.findByRole('button', { name: 'Role bindings' }));
    const sheet = await screen.findByRole('dialog', { name: 'Role bindings of alice' });
    await user.click(await within(sheet).findByRole('button', { name: 'Delete binding' }));

    const confirm = await screen.findByRole('alertdialog', { name: 'Delete role binding?' });
    const confirmButton = within(confirm).getByRole('button', { name: 'Delete' });
    expect(confirmButton).toBeDisabled();
    await user.type(within(confirm).getByLabelText('Type alice to confirm'), 'alice');
    await user.click(confirmButton);

    await waitFor(() =>
      expect(api.deleteRoleBinding).toHaveBeenCalledWith(expect.objectContaining({ id: 'rb_owner' })),
    );
    expect(await screen.findAllByText('cannot remove the last owner of the tenant')).not.toHaveLength(0);
  });
});
