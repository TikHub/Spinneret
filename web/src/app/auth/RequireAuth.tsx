import { Link, useRouter } from '@tanstack/react-router';
import { useEffect, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';

import { EmptyState } from '@/components/EmptyState';
import { ErrorState } from '@/components/ErrorState';
import { FullPageLoader } from '@/components/FullPageLoader';
import { Button } from '@/components/ui/button';

import { useAuth } from './AuthContext';

/** Route guard: waits for GetMe, redirects anonymous users to /login?redirect=<current URL>. */
export function RequireAuth({ children }: { children: ReactNode }) {
  const { t } = useTranslation();
  const router = useRouter();
  const { status, error, refresh } = useAuth();

  // Redirect once per transition to "anonymous". The current URL is read when the
  // effect runs (not during render) so the pending /login location is never fed
  // back into the redirect parameter.
  useEffect(() => {
    if (status !== 'anonymous') return;
    const href = router.state.location.href;
    if (href.startsWith('/login')) return;
    void router.navigate({
      to: '/login',
      search: { redirect: href === '/' ? undefined : href },
      replace: true,
    });
  }, [status, router]);

  if (status === 'loading') return <FullPageLoader label={t('shell.loadingSession')} />;
  if (status === 'error') {
    return (
      <div className="p-6">
        <ErrorState error={error} title={t('shell.sessionErrorTitle')} onRetry={() => void refresh()} />
      </div>
    );
  }
  if (status === 'anonymous') return <FullPageLoader />;
  return <>{children}</>;
}

/** Shown inside the shell when the user has no tenant access at all. */
export function NoTenantAccess() {
  const { t } = useTranslation();
  const { isPlatformAdmin } = useAuth();
  return (
    <EmptyState
      title={t('shell.noAccessTitle')}
      description={isPlatformAdmin ? t('shell.noTenantsAdminDescription') : t('shell.noAccessDescription')}
      action={
        isPlatformAdmin ? (
          <Button asChild>
            <Link to="/admin/tenants">{t('shell.manageTenants')}</Link>
          </Button>
        ) : undefined
      }
      className="mt-6"
    />
  );
}
