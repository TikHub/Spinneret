import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, renderHook, waitFor } from '@testing-library/react';
import { type ReactNode } from 'react';
import { describe, expect, it, vi } from 'vitest';

vi.mock('@/lib/clients', async () => {
  const { createClient, createRouterTransport } = await import('@connectrpc/connect');
  const { ProxyAdminService } = await import('@/gen/spinneret/v1/proxy_admin_pb');
  const transport = createRouterTransport(({ service }) => {
    service(ProxyAdminService, {
      updateProxy: (req) => ({ proxy: { id: req.id } }),
      importProxies: () => ({ created: 1 }),
    });
  });
  return { proxyClient: createClient(ProxyAdminService, transport), siteClient: {} };
});

vi.mock('@/app/auth/AuthContext', () => ({
  useAuth: () => ({ namespaceName: 'prod', tenantId: 'ten_1' }),
  useScopedQueryKey:
    () =>
    (domain: string, ...parts: unknown[]) => [domain, 'ten_1', 'prod', ...parts],
  useScopedPlaceholder: () => undefined,
}));

const { useImportProxies, useUpdateProxy } = await import('./useProxies');

const PROXY_URL = 'http://alice:hunter2@203.0.113.7:8080';

function setup() {
  const client = new QueryClient();
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  return { client, wrapper };
}

/** Serialized variables of every mutation still held by the cache. */
function cachedVariables(client: QueryClient): string {
  return JSON.stringify(
    client
      .getMutationCache()
      .getAll()
      .map((m) => m.state.variables),
  );
}

describe('proxy mutations carrying proxy credentials', () => {
  it('drops a replaced URL from the mutation cache once the edit dialog unmounts', async () => {
    const { client, wrapper } = setup();
    const { result, unmount } = renderHook(() => useUpdateProxy(), { wrapper });
    await act(() => result.current.mutateAsync({ id: 'prx_1', url: PROXY_URL, tags: [], setTags: false }));
    expect(cachedVariables(client)).toContain('hunter2');

    unmount();
    await waitFor(() => expect(client.getMutationCache().getAll()).toHaveLength(0));
  });

  it('drops imported proxy lines from the mutation cache once the import dialog unmounts', async () => {
    const { client, wrapper } = setup();
    const { result, unmount } = renderHook(() => useImportProxies(), { wrapper });
    await act(() =>
      result.current.mutateAsync({
        format: 'lines',
        data: `${PROXY_URL}\n`,
        dryRun: true,
        defaults: {
          kind: 'datacenter',
          region: '',
          city: '',
          provider: '',
          tags: [],
          maxConcurrency: 0,
          sessionTemplate: '',
        },
      }),
    );
    expect(cachedVariables(client)).toContain('hunter2');

    unmount();
    await waitFor(() => expect(client.getMutationCache().getAll()).toHaveLength(0));
  });
});
