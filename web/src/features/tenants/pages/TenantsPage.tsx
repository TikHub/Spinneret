import { Building2Icon, FolderTreeIcon } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { useAuth } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';
import { ErrorState } from '@/components/ErrorState';
import { PageHeader } from '@/components/PageHeader';
import { PageIntro } from '@/components/PageIntro';

import { NamespacesSection } from '../components/NamespacesSection';
import { TenantsSection } from '../components/TenantsSection';

/** Reads a translated string array, tolerating a missing or malformed value. */
function usePoints(key: string): string[] {
  const { t } = useTranslation('tenants');
  const raw: unknown = t(key, { returnObjects: true });
  if (!Array.isArray(raw)) return [];
  return raw.filter((item): item is string => typeof item === 'string');
}

/**
 * Two plain columns explaining the tenancy model: a tenant is the isolation
 * boundary of a team, a namespace partitions it into environments.
 */
function TenancyDiagram() {
  const { t } = useTranslation('tenants');
  const tenantPoints = usePoints('intro.tenantPoints');
  const namespacePoints = usePoints('intro.namespacePoints');
  const columns = [
    { icon: Building2Icon, title: t('intro.tenantTitle'), points: tenantPoints },
    { icon: FolderTreeIcon, title: t('intro.namespaceTitle'), points: namespacePoints },
  ];
  return (
    <div className="space-y-2 pt-1">
      <div className="grid gap-2 md:grid-cols-2">
        {columns.map(({ icon: Icon, title, points }) => (
          <div key={title} className="rounded-md border bg-background/60 p-3">
            <p className="flex items-center gap-2 text-sm font-medium text-foreground">
              <Icon className="size-4 shrink-0 text-muted-foreground" aria-hidden />
              <span className="min-w-0">{title}</span>
            </p>
            <ul className="mt-2 ml-4 list-disc space-y-1">
              {points.map((point) => (
                <li key={point}>{point}</li>
              ))}
            </ul>
          </div>
        ))}
      </div>
      <p className="font-medium text-foreground">{t('intro.mapping')}</p>
    </div>
  );
}

/**
 * Tenants (platform admins manage all of them) and the namespaces of the
 * active tenant (owners manage those). The route renders without tenants so a
 * platform admin can create the first one.
 */
export default function TenantsPage() {
  const { t } = useTranslation('tenants');
  const { isPlatformAdmin, canAny, canInTenant } = useAuth();
  // Owners of a tenant without namespaces have no active namespace, so check the tenant too.
  const allowed =
    isPlatformAdmin ||
    canInTenant(PERMISSIONS.namespaceRead) ||
    canAny([PERMISSIONS.namespaceRead, PERMISSIONS.namespaceWrite]);

  if (!allowed) {
    return (
      <ErrorState
        title={t('common:permission.deniedTitle')}
        description={t('common:permission.deniedDescription')}
        className="mt-6"
      />
    );
  }

  return (
    <>
      <PageHeader
        title={t('title')}
        description={isPlatformAdmin ? t('descriptionAdmin') : t('description')}
      />
      <PageIntro
        page="tenants"
        links={[
          { to: '/access/users', labelKey: 'nav.users' },
          { to: '/access/tokens', labelKey: 'nav.tokens' },
        ]}
      >
        <TenancyDiagram />
      </PageIntro>
      <div className="grid gap-4">
        <TenantsSection />
        <NamespacesSection />
      </div>
    </>
  );
}
