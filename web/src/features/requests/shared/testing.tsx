/* Test helpers for the observability pages; never imported by the app. */
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from '@tanstack/react-router';
import { render } from '@testing-library/react';
import { type ReactNode } from 'react';

import { TooltipProvider } from '@/components/ui/tooltip';

export const TEST_TENANT_ID = 'ten_1';
export const TEST_NAMESPACE = 'default';

function scopedKey(domain: string, ...parts: unknown[]): readonly unknown[] {
  return [domain, TEST_TENANT_ID, TEST_NAMESPACE, ...parts];
}

function keepPrevious<T>(previous: T | undefined): T | undefined {
  return previous;
}

/**
 * Replacement for '@/app/auth/AuthContext' in page tests:
 * vi.mock('@/app/auth/AuthContext', async () => (await import('.../testing')).authModuleMock()).
 */
export function authModuleMock(can: (permission: string, site?: string) => boolean = () => true) {
  const auth = {
    status: 'authenticated',
    tenantId: TEST_TENANT_ID,
    namespaceName: TEST_NAMESPACE,
    can,
    canAny: (permissions: readonly string[], site?: string) => permissions.some((p) => can(p, site)),
    canInTenant: (permission: string) => can(permission),
  };
  return {
    useAuth: () => auth,
    usePermission: (permission: string, site?: string) => can(permission, site),
    useScopedQueryKey: () => scopedKey,
    useScopedPlaceholder: () => keepPrevious,
  };
}

/** Renders a page component inside React Query, tooltips and a memory router with detail routes. */
export function renderWithRouter(Page: () => ReactNode) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute({ component: Outlet });
  const routes = [
    createRoute({ getParentRoute: () => rootRoute, path: '/', component: Page }),
    createRoute({
      getParentRoute: () => rootRoute,
      path: 'identities/$id',
      component: () => <p>identity detail</p>,
    }),
    createRoute({
      getParentRoute: () => rootRoute,
      path: 'risk-events',
      component: () => <p>risk events</p>,
    }),
  ];
  const router = createRouter({
    routeTree: rootRoute.addChildren(routes),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  });
  const result = render(
    <QueryClientProvider client={queryClient}>
      <TooltipProvider>
        {/* The test router is not the registered app router type. */}
        <RouterProvider router={router as never} />
      </TooltipProvider>
    </QueryClientProvider>,
  );
  return { ...result, router, queryClient };
}
