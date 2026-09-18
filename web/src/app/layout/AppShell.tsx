import { Outlet, useMatches } from '@tanstack/react-router';
import { Suspense, useState } from 'react';

import { useAuth } from '@/app/auth/AuthContext';
import { NoTenantAccess } from '@/app/auth/RequireAuth';
import { useConsoleEvents } from '@/app/events/useConsoleEvents';
import { FullPageLoader } from '@/components/FullPageLoader';
import { readStorage, STORAGE_KEYS, writeStorage } from '@/lib/storage';

import { RouteErrorBoundary } from './RouteErrorBoundary';
import { Sidebar } from './Sidebar';
import { Topbar } from './Topbar';

/** Authenticated layout: sidebar, topbar and the routed page. */
export function AppShell() {
  const { tenants } = useAuth();
  const tenantOptional = useMatches({
    select: (matches) => matches.some((m) => m.staticData?.tenantOptional),
  });
  const [collapsed, setCollapsed] = useState(() => readStorage(STORAGE_KEYS.sidebarCollapsed) === 'true');
  const liveStatus = useConsoleEvents();

  const toggle = () => {
    setCollapsed((prev) => {
      writeStorage(STORAGE_KEYS.sidebarCollapsed, String(!prev));
      return !prev;
    });
  };

  return (
    <div className="flex h-screen min-w-0 overflow-hidden bg-background">
      <Sidebar collapsed={collapsed} onToggle={toggle} />
      <div className="flex min-w-0 flex-1 flex-col">
        <Topbar liveStatus={liveStatus} />
        <main id="main" className="min-w-0 flex-1 overflow-y-auto">
          <div className="mx-auto w-full max-w-[1600px] px-6 py-5">
            {tenants.length === 0 && !tenantOptional ? (
              <NoTenantAccess />
            ) : (
              <RouteErrorBoundary>
                <Suspense fallback={<FullPageLoader />}>
                  <Outlet />
                </Suspense>
              </RouteErrorBoundary>
            )}
          </div>
        </main>
      </div>
    </div>
  );
}
