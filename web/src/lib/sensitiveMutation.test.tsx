import { QueryClient, QueryClientProvider, useMutation } from '@tanstack/react-query';
import { act, renderHook, waitFor } from '@testing-library/react';
import { type ReactNode } from 'react';
import { describe, expect, it, vi } from 'vitest';

import { isSensitiveMutation, sensitiveMutation } from './sensitiveMutation';

interface Credentials {
  username: string;
  password: string;
}

function setup() {
  const client = new QueryClient();
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  return { client, wrapper };
}

/** Variables and results of every mutation still held by the cache. */
function cachedPayloads(client: QueryClient): unknown[] {
  return client
    .getMutationCache()
    .getAll()
    .flatMap((m) => [m.state.variables, m.state.data]);
}

const login = async (vars: Credentials) => ({ session: `session-of-${vars.username}`, secret: 'spn_plain' });

describe('sensitiveMutation', () => {
  it('forces gcTime 0 and no retry while keeping the caller options', () => {
    const onSuccess = vi.fn();
    const options = sensitiveMutation({
      mutationFn: login,
      onSuccess,
      retry: 3,
      gcTime: 60_000,
      meta: { source: 'login' },
    });
    expect(options.gcTime).toBe(0);
    expect(options.retry).toBe(false);
    expect(options.onSuccess).toBe(onSuccess);
    expect(options.mutationFn).toBe(login);
    expect(options.meta).toEqual({ source: 'login', sensitive: true });
    expect(isSensitiveMutation(options.meta)).toBe(true);
    expect(isSensitiveMutation({ source: 'login' })).toBe(false);
    expect(isSensitiveMutation(undefined)).toBe(false);
  });

  it('drops credentials and results from the mutation cache once the form unmounts', async () => {
    const { client, wrapper } = setup();
    const { result, unmount } = renderHook(() => useMutation(sensitiveMutation({ mutationFn: login })), {
      wrapper,
    });
    act(() => result.current.mutate({ username: 'alice', password: 'hunter2' }));
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    // Still observed: the form can show the one-time result.
    expect(result.current.data?.secret).toBe('spn_plain');

    unmount();
    await waitFor(() => expect(client.getMutationCache().getAll()).toHaveLength(0));
    expect(JSON.stringify(cachedPayloads(client))).not.toContain('hunter2');
  });

  it('drops the previous attempt when the form submits again', async () => {
    const { client, wrapper } = setup();
    const { result } = renderHook(() => useMutation(sensitiveMutation({ mutationFn: login })), { wrapper });
    act(() => result.current.mutate({ username: 'alice', password: 'first-try' }));
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    act(() => result.current.mutate({ username: 'alice', password: 'second-try' }));
    await waitFor(() => expect(result.current.variables?.password).toBe('second-try'));
    await waitFor(() => expect(client.getMutationCache().getAll()).toHaveLength(1));
    expect(JSON.stringify(cachedPayloads(client))).not.toContain('first-try');
  });

  it('differs from a plain mutation, which keeps the variables after unmount', async () => {
    const { client, wrapper } = setup();
    const { result, unmount } = renderHook(() => useMutation({ mutationFn: login }), { wrapper });
    act(() => result.current.mutate({ username: 'alice', password: 'hunter2' }));
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    unmount();
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(JSON.stringify(cachedPayloads(client))).toContain('hunter2');
    client.clear();
  });
});
