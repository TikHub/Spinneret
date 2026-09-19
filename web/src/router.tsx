import { createRootRoute, createRoute, createRouter, lazyRouteComponent } from '@tanstack/react-router';

import '@/app/routeMeta';
import { AuthenticatedLayout, RootLayout, RouteError } from '@/app/layout/RootLayout';
import { NotFoundPage } from '@/app/pages/NotFoundPage';
import { FullPageLoader } from '@/components/FullPageLoader';

/**
 * Loosely typed search params for feature pages. Pages read and validate the
 * keys they use, e.g. `useSearch({ from: '/_app/identities' }).site`.
 */
export type PageSearch = Record<string, string | number | boolean | string[] | undefined>;

function pageSearch(search: Record<string, unknown>): PageSearch {
  return search as PageSearch;
}

/** Search params of /login. */
export interface LoginSearch {
  /** Same-origin path to return to after signing in. */
  redirect?: string;
}

function loginSearch(search: Record<string, unknown>): LoginSearch {
  return typeof search.redirect === 'string' && search.redirect !== '' ? { redirect: search.redirect } : {};
}

const rootRoute = createRootRoute({
  component: RootLayout,
  notFoundComponent: NotFoundPage,
});

const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: 'login',
  validateSearch: loginSearch,
  staticData: { titleKey: 'nav.login' },
  component: lazyRouteComponent(() => import('@/features/auth/pages/LoginPage')),
});

/** Pathless layout route for every authenticated page (id "_app"). */
const appRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: '_app',
  component: AuthenticatedLayout,
});

const overviewRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/',
  staticData: { titleKey: 'nav.overview' },
  component: lazyRouteComponent(() => import('@/features/overview/pages/OverviewPage')),
});

const identitiesRoute = createRoute({
  getParentRoute: () => appRoute,
  path: 'identities',
  validateSearch: pageSearch,
  staticData: { titleKey: 'nav.identities', groupKey: 'nav.groups.scheduling' },
  component: lazyRouteComponent(() => import('@/features/identities/pages/IdentitiesPage')),
});

const identityDetailRoute = createRoute({
  getParentRoute: () => appRoute,
  path: 'identities/$id',
  validateSearch: pageSearch,
  staticData: {
    titleKey: 'nav.identityDetail',
    groupKey: 'nav.groups.scheduling',
    parent: { to: '/identities', titleKey: 'nav.identities' },
  },
  component: lazyRouteComponent(() => import('@/features/identities/pages/IdentityDetailPage')),
});

const identityTypesRoute = createRoute({
  getParentRoute: () => appRoute,
  path: 'identity-types',
  validateSearch: pageSearch,
  staticData: { titleKey: 'nav.identityTypes', groupKey: 'nav.groups.scheduling' },
  component: lazyRouteComponent(() => import('@/features/identity-types/pages/IdentityTypesPage')),
});

const accountsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: 'accounts',
  validateSearch: pageSearch,
  staticData: { titleKey: 'nav.accounts', groupKey: 'nav.groups.scheduling' },
  component: lazyRouteComponent(() => import('@/features/accounts/pages/AccountsPage')),
});

const proxiesRoute = createRoute({
  getParentRoute: () => appRoute,
  path: 'proxies',
  validateSearch: pageSearch,
  staticData: { titleKey: 'nav.proxies', groupKey: 'nav.groups.scheduling' },
  component: lazyRouteComponent(() => import('@/features/proxies/pages/ProxiesPage')),
});

const sitesRoute = createRoute({
  getParentRoute: () => appRoute,
  path: 'sites',
  validateSearch: pageSearch,
  staticData: { titleKey: 'nav.sites', groupKey: 'nav.groups.scheduling' },
  component: lazyRouteComponent(() => import('@/features/sites/pages/SitesPage')),
});

const policiesRoute = createRoute({
  getParentRoute: () => appRoute,
  path: 'policies',
  validateSearch: pageSearch,
  staticData: { titleKey: 'nav.policies', groupKey: 'nav.groups.scheduling' },
  component: lazyRouteComponent(() => import('@/features/policies/pages/PoliciesPage')),
});

const heatmapRoute = createRoute({
  getParentRoute: () => appRoute,
  path: 'heatmap',
  validateSearch: pageSearch,
  staticData: { titleKey: 'nav.heatmap', groupKey: 'nav.groups.scheduling' },
  component: lazyRouteComponent(() => import('@/features/heatmap/pages/HeatmapPage')),
});

const breakersRoute = createRoute({
  getParentRoute: () => appRoute,
  path: 'breakers',
  validateSearch: pageSearch,
  staticData: { titleKey: 'nav.breakers', groupKey: 'nav.groups.scheduling' },
  component: lazyRouteComponent(() => import('@/features/breakers/pages/BreakersPage')),
});

const configRoute = createRoute({
  getParentRoute: () => appRoute,
  path: 'config',
  validateSearch: pageSearch,
  staticData: { titleKey: 'nav.config', groupKey: 'nav.groups.configuration' },
  component: lazyRouteComponent(() => import('@/features/config/pages/ConfigPage')),
});

const secretsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: 'secrets',
  validateSearch: pageSearch,
  staticData: { titleKey: 'nav.secrets', groupKey: 'nav.groups.configuration' },
  component: lazyRouteComponent(() => import('@/features/secrets/pages/SecretsPage')),
});

const requestsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: 'requests',
  validateSearch: pageSearch,
  staticData: { titleKey: 'nav.requests', groupKey: 'nav.groups.observability' },
  component: lazyRouteComponent(() => import('@/features/requests/pages/RequestsPage')),
});

const riskEventsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: 'risk-events',
  validateSearch: pageSearch,
  staticData: { titleKey: 'nav.riskEvents', groupKey: 'nav.groups.observability' },
  component: lazyRouteComponent(() => import('@/features/risk-events/pages/RiskEventsPage')),
});

const notificationsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: 'notifications',
  validateSearch: pageSearch,
  staticData: { titleKey: 'nav.notifications', groupKey: 'nav.groups.observability' },
  component: lazyRouteComponent(() => import('@/features/notifications/pages/NotificationsPage')),
});

const tokensRoute = createRoute({
  getParentRoute: () => appRoute,
  path: 'access/tokens',
  validateSearch: pageSearch,
  staticData: { titleKey: 'nav.tokens', groupKey: 'nav.groups.access' },
  component: lazyRouteComponent(() => import('@/features/access/pages/TokensPage')),
});

const usersRoute = createRoute({
  getParentRoute: () => appRoute,
  path: 'access/users',
  validateSearch: pageSearch,
  staticData: { titleKey: 'nav.users', groupKey: 'nav.groups.access' },
  component: lazyRouteComponent(() => import('@/features/access/pages/UsersPage')),
});

const auditRoute = createRoute({
  getParentRoute: () => appRoute,
  path: 'access/audit',
  validateSearch: pageSearch,
  staticData: { titleKey: 'nav.audit', groupKey: 'nav.groups.access' },
  component: lazyRouteComponent(() => import('@/features/access/pages/AuditPage')),
});

const tenantsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: 'admin/tenants',
  validateSearch: pageSearch,
  staticData: { titleKey: 'nav.tenants', groupKey: 'nav.groups.platform', tenantOptional: true },
  component: lazyRouteComponent(() => import('@/features/tenants/pages/TenantsPage')),
});

const profileRoute = createRoute({
  getParentRoute: () => appRoute,
  path: 'settings/profile',
  staticData: { titleKey: 'nav.profile', groupKey: 'nav.groups.settings', tenantOptional: true },
  component: lazyRouteComponent(() => import('@/features/settings/pages/ProfilePage')),
});

const systemRoute = createRoute({
  getParentRoute: () => appRoute,
  path: 'settings/system',
  staticData: { titleKey: 'nav.system', groupKey: 'nav.groups.settings', tenantOptional: true },
  component: lazyRouteComponent(() => import('@/features/settings/pages/SystemPage')),
});

export const routeTree = rootRoute.addChildren([
  loginRoute,
  appRoute.addChildren([
    overviewRoute,
    identitiesRoute,
    identityDetailRoute,
    identityTypesRoute,
    accountsRoute,
    proxiesRoute,
    sitesRoute,
    policiesRoute,
    heatmapRoute,
    breakersRoute,
    configRoute,
    secretsRoute,
    requestsRoute,
    riskEventsRoute,
    notificationsRoute,
    tokensRoute,
    usersRoute,
    auditRoute,
    tenantsRoute,
    profileRoute,
    systemRoute,
  ]),
]);

export const router = createRouter({
  routeTree,
  defaultPreload: 'intent',
  defaultPreloadStaleTime: 0,
  defaultPendingComponent: () => <FullPageLoader />,
  defaultErrorComponent: RouteError,
  defaultNotFoundComponent: NotFoundPage,
  scrollRestoration: true,
});

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router;
  }
}
