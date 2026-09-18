import { create } from '@bufbuild/protobuf';
import { Code, ConnectError } from '@connectrpc/connect';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, renderHook, waitFor } from '@testing-library/react';
import { type ReactNode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import {
  GetMeResponseSchema,
  NamespaceAccessSchema,
  NamespaceSchema,
  RoleBindingSchema,
  SiteAccessSchema,
  TenantAccessSchema,
  TenantSchema,
  UserSchema,
} from '@/gen/spinneret/v1/auth_pb';

const getMe = vi.fn();
const logout = vi.fn();

vi.mock('@/lib/clients', () => ({
  authClient: {
    getMe: (...args: unknown[]) => getMe(...args),
    logout: (...args: unknown[]) => logout(...args),
    login: vi.fn(),
  },
}));

const { AuthProvider, useAuth } = await import('./AuthContext');

function meResponse(isPlatformAdmin = false) {
  return create(GetMeResponseSchema, {
    user: create(UserSchema, { id: 'usr_1', username: 'ops', isPlatformAdmin }),
    tenants: [
      create(TenantAccessSchema, {
        tenant: create(TenantSchema, { id: 'ten_1', name: 'acme' }),
        namespaces: [
          create(NamespaceAccessSchema, {
            namespace: create(NamespaceSchema, { id: 'ns_1', name: 'default' }),
            permissions: ['dashboard:read', 'site:read'],
            sites: [
              create(SiteAccessSchema, { siteId: 'sit_a', siteName: 'a', permissions: ['identity:operate'] }),
            ],
          }),
          create(NamespaceAccessSchema, {
            namespace: create(NamespaceSchema, { id: 'ns_2', name: 'staging' }),
            permissions: ['config:publish'],
          }),
        ],
      }),
    ],
  });
}

function wrapper() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>
      <AuthProvider>{children}</AuthProvider>
    </QueryClientProvider>
  );
}

describe('AuthContext', () => {
  beforeEach(() => {
    window.localStorage.clear();
  });

  afterEach(() => {
    getMe.mockReset();
    logout.mockReset();
  });

  it('computes can() for the active namespace and follows namespace switches', async () => {
    getMe.mockResolvedValue(meResponse());
    const { result } = renderHook(() => useAuth(), { wrapper: wrapper() });

    await waitFor(() => expect(result.current.status).toBe('authenticated'));
    expect(result.current.tenantId).toBe('ten_1');
    expect(result.current.namespaceName).toBe('default');
    expect(result.current.can('dashboard:read')).toBe(true);
    expect(result.current.can('identity:operate', 'sit_a')).toBe(true);
    expect(result.current.can('identity:operate', 'sit_b')).toBe(false);
    expect(result.current.can('config:publish')).toBe(false);
    expect(result.current.canAny(['config:publish', 'site:read'])).toBe(true);

    act(() => result.current.setNamespace('staging'));
    expect(result.current.namespaceName).toBe('staging');
    expect(result.current.can('config:publish')).toBe(true);
    expect(result.current.can('dashboard:read')).toBe(false);
    expect(window.localStorage.getItem('spinneret.namespace')).toBe('staging');
    expect(window.localStorage.getItem('spinneret.tenant')).toBe('ten_1');
  });

  it('checks tenant-level permissions against tenant-wide bindings', async () => {
    const pinnedOwner = meResponse();
    const tenant = pinnedOwner.tenants[0]!;
    tenant.bindings = [
      create(RoleBindingSchema, { role: 'owner', tenantId: 'ten_1', namespaceId: 'ns_1' }),
      create(RoleBindingSchema, { role: 'viewer', tenantId: 'ten_1' }),
    ];
    getMe.mockResolvedValue(pinnedOwner);
    const { result } = renderHook(() => useAuth(), { wrapper: wrapper() });
    await waitFor(() => expect(result.current.status).toBe('authenticated'));
    expect(result.current.canInTenant('user:read')).toBe(false);
    expect(result.current.canInTenant('notify:read')).toBe(true);
  });

  it('grants everything to platform admins', async () => {
    getMe.mockResolvedValue(meResponse(true));
    const { result } = renderHook(() => useAuth(), { wrapper: wrapper() });
    await waitFor(() => expect(result.current.status).toBe('authenticated'));
    expect(result.current.can('tenant:manage')).toBe(true);
    expect(result.current.can('secret:reveal', 'sit_zzz')).toBe(true);
    expect(result.current.canInTenant('user:write')).toBe(true);
  });

  it('is anonymous when GetMe is unauthenticated, and after logout', async () => {
    getMe.mockRejectedValueOnce(new ConnectError('no session', Code.Unauthenticated));
    const { result } = renderHook(() => useAuth(), { wrapper: wrapper() });
    await waitFor(() => expect(result.current.status).toBe('anonymous'));
    expect(result.current.can('dashboard:read')).toBe(false);

    getMe.mockResolvedValue(meResponse());
    await act(() => result.current.refresh());
    await waitFor(() => expect(result.current.status).toBe('authenticated'));

    logout.mockResolvedValue({});
    await act(() => result.current.logout());
    await waitFor(() => expect(result.current.status).toBe('anonymous'));
    expect(logout).toHaveBeenCalledOnce();
  });

  it('reports other GetMe failures as errors', async () => {
    getMe.mockRejectedValue(new ConnectError('schema not migrated', Code.FailedPrecondition));
    const { result } = renderHook(() => useAuth(), { wrapper: wrapper() });
    await waitFor(() => expect(result.current.status).toBe('error'));
  });
});
