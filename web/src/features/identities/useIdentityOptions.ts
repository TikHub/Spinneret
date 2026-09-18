import { useQuery } from '@tanstack/react-query';

import { useAuth, useScopedQueryKey } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';
import { identityClient, policyClient, siteClient } from '@/lib/clients';

/** Page size used to load option lists (the API maximum). */
export const OPTIONS_PAGE_SIZE = 500;
/** Option lists change rarely. */
export const OPTIONS_STALE_MS = 60_000;

/** Sites of the active namespace for selects. */
export function useSiteOptions(enabled = true) {
  const { namespaceName } = useAuth();
  const key = useScopedQueryKey();
  return useQuery({
    queryKey: key('sites', 'options'),
    queryFn: ({ signal }) =>
      siteClient.listSites({ namespace: namespaceName ?? '', pageSize: OPTIONS_PAGE_SIZE }, { signal }),
    enabled: enabled && Boolean(namespaceName),
    staleTime: OPTIONS_STALE_MS,
    select: (res) => res.sites,
  });
}

/** Identity types of a site (all accessible sites when empty) for selects. */
export function useIdentityTypeOptions(site: string, enabled = true) {
  const { namespaceName } = useAuth();
  const key = useScopedQueryKey();
  return useQuery({
    queryKey: key('identity-types', 'options', site),
    queryFn: ({ signal }) =>
      identityClient.listIdentityTypes(
        { namespace: namespaceName ?? '', site, pageSize: OPTIONS_PAGE_SIZE },
        { signal },
      ),
    enabled: enabled && Boolean(namespaceName),
    staleTime: OPTIONS_STALE_MS,
    select: (res) => res.identityTypes,
  });
}

/** Endpoint groups of a site for selects; disabled until a site is chosen. */
export function useEndpointGroupOptions(site: string, enabled = true) {
  const { namespaceName } = useAuth();
  const key = useScopedQueryKey();
  return useQuery({
    queryKey: key('sites', 'endpoint-groups', 'options', site),
    queryFn: ({ signal }) =>
      siteClient.listEndpointGroups(
        { namespace: namespaceName ?? '', site, pageSize: OPTIONS_PAGE_SIZE },
        { signal },
      ),
    enabled: enabled && Boolean(namespaceName) && site !== '',
    staleTime: OPTIONS_STALE_MS,
    select: (res) => res.endpointGroups,
  });
}

/** Action policies for the revert dialog; disabled without policy:read. */
export function useActionPolicyOptions(enabled = true) {
  const { namespaceName, can } = useAuth();
  const key = useScopedQueryKey();
  const allowed = can(PERMISSIONS.policyRead);
  const query = useQuery({
    queryKey: key('policies', 'options', 'action'),
    queryFn: ({ signal }) =>
      policyClient.listPolicies(
        { namespace: namespaceName ?? '', kind: 'action', pageSize: OPTIONS_PAGE_SIZE },
        { signal },
      ),
    enabled: enabled && allowed && Boolean(namespaceName),
    staleTime: OPTIONS_STALE_MS,
    select: (res) => res.policies,
  });
  return { ...query, allowed };
}
