import { QueryClient } from '@tanstack/react-query';

import { shouldRetryQuery } from '@/lib/errors';

/** Default freshness for console data. */
export const DEFAULT_STALE_TIME_MS = 5_000;
/** Refetch interval for pages that show live state (identities, proxies, breakers). */
export const LIVE_REFETCH_MS = 5_000;
/** Refetch interval for the overview dashboard. */
export const OVERVIEW_REFETCH_MS = 10_000;

/** Creates the QueryClient with console defaults. */
export function createQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: DEFAULT_STALE_TIME_MS,
        retry: shouldRetryQuery,
        retryDelay: (attempt) => Math.min(1000 * 2 ** attempt, 8000),
        refetchOnWindowFocus: false,
      },
      mutations: {
        retry: false,
      },
    },
  });
}
