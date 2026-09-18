import { Outlet, type ErrorComponentProps } from '@tanstack/react-router';

import { RequireAuth } from '@/app/auth/RequireAuth';
import { AppErrorFallback } from '@/app/errors/AppErrorFallback';

import { AppShell } from './AppShell';
import { DocumentTitle } from './DocumentTitle';

/** Root route component: document title sync plus the matched route. */
export function RootLayout() {
  return (
    <>
      <DocumentTitle />
      <Outlet />
    </>
  );
}

/** Layout of every authenticated route: session guard and application shell. */
export function AuthenticatedLayout() {
  return (
    <RequireAuth>
      <AppShell />
    </RequireAuth>
  );
}

/** Router error component (errors thrown while loading or rendering a route). */
export function RouteError({ error, reset }: ErrorComponentProps) {
  return <AppErrorFallback error={error} onReset={reset} />;
}
