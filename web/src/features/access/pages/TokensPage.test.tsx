import { create } from '@bufbuild/protobuf';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import {
  ApiTokenSchema,
  type CreateTokenRequest,
  type ListTokensRequest,
} from '@/gen/spinneret/v1/access_admin_pb';

const api = vi.hoisted(() => ({
  getMe: vi.fn(),
  listTokens: vi.fn(),
  createToken: vi.fn(),
  revokeToken: vi.fn(),
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
      listTokens: (req) => api.listTokens(req),
      createToken: (req) => api.createToken(req),
      revokeToken: (req) => api.revokeToken(req),
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
const { default: TokensPage } = await import('./TokensPage');

const existing = create(ApiTokenSchema, {
  id: 'tok_1',
  namespace: 'prod',
  name: 'crawler-01',
  tokenPrefix: 'spn_abcdefgh',
  scopes: ['lease:acquire:shop', 'report:write', 'secret:read:prod/signing/*'],
  ipAllowlist: ['10.0.0.0/8'],
  rateLimitRps: 50,
  createdBy: 'user:usr_me',
});

describe('TokensPage', () => {
  beforeEach(() => {
    window.localStorage.clear();
    api.getMe.mockResolvedValue(meResponse({ permissions: ['token:read', 'token:write', 'site:read'] }));
    api.listTokens.mockResolvedValue({ tokens: [existing], total: 1 });
    api.listSites.mockResolvedValue({ sites: [{ name: 'shop' }, { name: 'market' }] });
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it('lists tokens of the active namespace with parsed scopes', async () => {
    renderWithProviders(<TokensPage />);

    expect(await screen.findByText('crawler-01')).toBeInTheDocument();
    expect(screen.getByText('spn_abcdefgh…')).toBeInTheDocument();
    expect(screen.getByText('lease:acquire')).toBeInTheDocument();
    expect(screen.getByText(':shop')).toBeInTheDocument();
    expect(screen.getByText('10.0.0.0/8')).toBeInTheDocument();
    expect(screen.getByText('50 req/s')).toBeInTheDocument();
    const request = api.listTokens.mock.calls[0]?.[0] as ListTokensRequest;
    expect(request.namespace).toBe('prod');
    expect(request.includeRevoked).toBe(false);
  });

  it('creates a token from a preset and shows the plaintext once', async () => {
    const user = userEvent.setup();
    api.createToken.mockImplementation((req: CreateTokenRequest) =>
      Promise.resolve({
        token: { ...existing, id: 'tok_2', name: req.name, scopes: req.scopes },
        plaintext: 'spn_secretvalue1234567890',
      }),
    );
    const { queryClient } = renderWithProviders(<TokensPage />);

    await user.click(await screen.findByRole('button', { name: 'Create token' }));
    const dialog = await screen.findByRole('dialog', { name: 'Create API token' });
    await user.type(within(dialog).getByLabelText(/^Name/), 'node-02');
    await user.click(within(dialog).getByRole('button', { name: 'Crawler node' }));
    await user.clear(within(dialog).getByLabelText('Expires in'));
    await user.type(within(dialog).getByLabelText('IP allowlist'), '203.0.113.7{Enter}');
    await user.click(within(dialog).getByRole('button', { name: 'Create token' }));

    await waitFor(() => expect(api.createToken).toHaveBeenCalledOnce());
    const request = api.createToken.mock.calls[0]?.[0] as CreateTokenRequest;
    expect(request).toMatchObject({
      namespace: 'prod',
      name: 'node-02',
      scopes: ['lease:acquire', 'report:write', 'config:read'],
      ipAllowlist: ['203.0.113.7'],
      rateLimitRps: 0,
    });
    expect(request.expiresAt).toBeUndefined();

    const secret = await screen.findByRole('dialog', { name: 'Token node-02 created' });
    expect(within(secret).getByText('spn_secretvalue1234567890')).toBeInTheDocument();
    // The plaintext must be reachable by its accessible name; ARIA ignores a
    // label on a <code> element, so the group around it carries the label.
    expect(within(secret).getByRole('group', { name: 'Token' })).toHaveTextContent(
      'spn_secretvalue1234567890',
    );
    const done = within(secret).getByRole('button', { name: 'Done' });
    expect(done).toBeDisabled();
    await user.keyboard('{Escape}');
    expect(screen.getByRole('dialog', { name: 'Token node-02 created' })).toBeInTheDocument();

    await user.click(within(secret).getByLabelText('I have saved the token in a safe place'));
    expect(done).toBeEnabled();
    await user.click(done);
    await waitFor(() => expect(screen.queryByText('spn_secretvalue1234567890')).not.toBeInTheDocument());
    // The plaintext never outlives the dialogs in the mutation cache.
    const cached = queryClient
      .getMutationCache()
      .getAll()
      .map((m) => m.state.data);
    expect(
      JSON.stringify(cached, (_key, value: unknown) => (typeof value === 'bigint' ? String(value) : value)),
    ).not.toContain('spn_secretvalue1234567890');
  });

  it('blocks submission while a required secret path is missing', async () => {
    const user = userEvent.setup();
    renderWithProviders(<TokensPage />);

    await user.click(await screen.findByRole('button', { name: 'Create token' }));
    const dialog = await screen.findByRole('dialog', { name: 'Create API token' });
    await user.type(within(dialog).getByLabelText(/^Name/), 'reader');
    await user.click(within(dialog).getByRole('button', { name: 'Create token' }));
    expect(await within(dialog).findByText('Add at least one scope.')).toBeInTheDocument();
    expect(api.createToken).not.toHaveBeenCalled();
  });

  it('disables token actions without token:write', async () => {
    api.getMe.mockResolvedValue(meResponse({ role: 'viewer', permissions: ['token:read'] }));
    renderWithProviders(<TokensPage />);

    expect(await screen.findByText('crawler-01')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Create token' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Revoke' })).toBeDisabled();
  });
});
