import { Code, ConnectError } from '@connectrpc/connect';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { type CreateNamespaceRequest, type CreateTenantRequest } from '@/gen/spinneret/v1/tenant_admin_pb';

const api = vi.hoisted(() => ({
  getMe: vi.fn(),
  listTenants: vi.fn(),
  createTenant: vi.fn(),
  listNamespaces: vi.fn(),
  createNamespace: vi.fn(),
  deleteNamespace: vi.fn(),
}));

vi.mock('@/lib/clients', async () => {
  const { createClient, createRouterTransport } = await import('@connectrpc/connect');
  const { AuthService } = await import('@/gen/spinneret/v1/auth_pb');
  const { TenantAdminService } = await import('@/gen/spinneret/v1/tenant_admin_pb');
  const transport = createRouterTransport((router) => {
    router.service(AuthService, { getMe: () => api.getMe() });
    router.service(TenantAdminService, {
      listTenants: (req) => api.listTenants(req),
      createTenant: (req) => api.createTenant(req),
      listNamespaces: (req) => api.listNamespaces(req),
      createNamespace: (req) => api.createNamespace(req),
      deleteNamespace: (req) => api.deleteNamespace(req),
    });
  });
  return {
    authClient: createClient(AuthService, transport),
    tenantClient: createClient(TenantAdminService, transport),
  };
});

const { meResponse, renderWithProviders } = await import('@/features/access/testing/providers');
const { default: TenantsPage } = await import('./TenantsPage');

describe('TenantsPage', () => {
  beforeEach(() => {
    window.localStorage.clear();
    api.getMe.mockResolvedValue(meResponse({ isPlatformAdmin: true }));
    api.listTenants.mockResolvedValue({
      tenants: [
        { id: 'ten_1', name: 'acme', displayName: 'Acme' },
        { id: 'ten_2', name: 'globex', displayName: 'Globex' },
      ],
      total: 2,
    });
    api.listNamespaces.mockResolvedValue({
      namespaces: [{ id: 'ns_1', tenantId: 'ten_1', name: 'prod', displayName: 'Production' }],
      total: 1,
    });
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it('lists tenants and the namespaces of the active tenant', async () => {
    renderWithProviders(<TenantsPage />);
    expect(await screen.findByText('globex')).toBeInTheDocument();
    expect(await screen.findByText('Production')).toBeInTheDocument();
    expect(screen.getByText('Namespaces of Acme')).toBeInTheDocument();
  });

  it('creates a tenant with a suggested slug and refreshes the session', async () => {
    const user = userEvent.setup();
    api.createTenant.mockImplementation((req: CreateTenantRequest) =>
      Promise.resolve({ tenant: { id: 'ten_3', name: req.name, displayName: req.displayName } }),
    );
    renderWithProviders(<TenantsPage />);

    await user.click(await screen.findByRole('button', { name: 'Create tenant' }));
    const dialog = await screen.findByRole('dialog', { name: 'Create tenant' });
    await user.type(within(dialog).getByLabelText('Display name'), 'Growth Team');
    expect(within(dialog).getByLabelText(/^Name \(slug\)/)).toHaveValue('growth-team');
    await user.type(within(dialog).getByLabelText('Owner user ID'), 'usr_owner');
    const getMeCalls = api.getMe.mock.calls.length;
    await user.click(within(dialog).getByRole('button', { name: 'Create' }));

    await waitFor(() => expect(api.createTenant).toHaveBeenCalledOnce());
    expect(api.createTenant.mock.calls[0]?.[0] as CreateTenantRequest).toMatchObject({
      name: 'growth-team',
      displayName: 'Growth Team',
      ownerUserId: 'usr_owner',
    });
    await waitFor(() => expect(api.getMe.mock.calls.length).toBeGreaterThan(getMeCalls));
    await waitFor(() =>
      expect(screen.queryByRole('dialog', { name: 'Create tenant' })).not.toBeInTheDocument(),
    );
  });

  it('rejects invalid namespace slugs before calling the server', async () => {
    const user = userEvent.setup();
    renderWithProviders(<TenantsPage />);

    await user.click(await screen.findByRole('button', { name: 'Create namespace' }));
    const dialog = await screen.findByRole('dialog', { name: 'Create namespace' });
    await user.type(within(dialog).getByLabelText(/^Name \(slug\)/), 'Bad_Name');
    await user.click(within(dialog).getByRole('button', { name: 'Create' }));
    expect(await within(dialog).findByText(/Use 2–63 lower-case letters/)).toBeInTheDocument();
    expect(api.createNamespace).not.toHaveBeenCalled();

    await user.clear(within(dialog).getByLabelText(/^Name \(slug\)/));
    await user.type(within(dialog).getByLabelText(/^Name \(slug\)/), 'staging');
    api.createNamespace.mockImplementation((req: CreateNamespaceRequest) =>
      Promise.resolve({ namespace: { id: 'ns_2', name: req.name } }),
    );
    await user.click(within(dialog).getByRole('button', { name: 'Create' }));
    await waitFor(() =>
      expect(api.createNamespace).toHaveBeenCalledWith(expect.objectContaining({ name: 'staging' })),
    );
  });

  it('keeps the delete dialog open with the reason when a namespace is not empty', async () => {
    const user = userEvent.setup();
    api.deleteNamespace.mockRejectedValue(
      new ConnectError('namespace still contains 3 sites', Code.FailedPrecondition),
    );
    renderWithProviders(<TenantsPage />);

    await screen.findByText('Production');
    const deleteButtons = screen.getAllByRole('button', { name: 'Delete' });
    // The second table (namespaces) holds the last delete button.
    await user.click(deleteButtons[deleteButtons.length - 1]!);
    const confirm = await screen.findByRole('alertdialog', { name: 'Delete namespace prod?' });
    await user.type(within(confirm).getByLabelText('Type prod to confirm'), 'prod');
    await user.click(within(confirm).getByRole('button', { name: 'Delete' }));

    await waitFor(() => expect(api.deleteNamespace).toHaveBeenCalledOnce());
    expect(await within(confirm).findByText('namespace still contains 3 sites')).toBeInTheDocument();
    expect(screen.getByRole('alertdialog', { name: 'Delete namespace prod?' })).toBeInTheDocument();
  });
});
