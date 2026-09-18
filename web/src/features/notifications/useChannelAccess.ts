import { useCallback } from 'react';

import { useAuth } from '@/app/auth/AuthContext';
import { checkPermission, checkTenantPermission } from '@/app/auth/permissions';

/**
 * Permission check for one channel: tenant-wide channels need a tenant-level
 * grant; namespace channels a grant in their own namespace (which may differ
 * from the active one when listing every channel of the tenant).
 */
export function useChannelAccess() {
  const { isPlatformAdmin, tenant, namespaces } = useAuth();
  return useCallback(
    (channelNamespace: string, permission: string): boolean => {
      if (channelNamespace === '') return checkTenantPermission({ isPlatformAdmin, tenant }, permission);
      const namespace = namespaces.find((n) => n.namespace?.name === channelNamespace);
      return checkPermission({ isPlatformAdmin, namespace, bindings: tenant?.bindings }, permission);
    },
    [isPlatformAdmin, tenant, namespaces],
  );
}
