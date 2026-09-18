import { useCallback, useMemo } from 'react';

import { useAuth } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';

/**
 * Permission checks of the sites page. Creating, editing and deleting sites
 * needs a namespace-wide site:write grant (the API checks the namespace
 * resource), while endpoint groups, URI rules and the site switch accept
 * per-site grants.
 */
export function useSitePermissions() {
  const { can, isPlatformAdmin, namespace } = useAuth();
  const namespacePermissions = namespace?.permissions;
  const canManageSites = isPlatformAdmin || (namespacePermissions?.includes(PERMISSIONS.siteWrite) ?? false);
  const canWriteSite = useCallback((site: string) => can(PERMISSIONS.siteWrite, site), [can]);
  const canPauseSite = useCallback((site: string) => can(PERMISSIONS.breakerOperate, site), [can]);
  return useMemo(
    () => ({ canManageSites, canWriteSite, canPauseSite }),
    [canManageSites, canWriteSite, canPauseSite],
  );
}
