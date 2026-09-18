import { useQuery } from '@tanstack/react-query';

import { useAuth, useScopedQueryKey } from '@/app/auth/AuthContext';
import { siteClient } from '@/lib/clients';

/** Largest page the site admin lists accept; filters show at most this many options. */
export const OPTIONS_PAGE_SIZE = 500;
/** Option lists change rarely. */
const OPTIONS_STALE_TIME_MS = 60_000;

/** Accessible sites of the active namespace (for filter selects). */
export function useSiteOptions() {
  const { namespaceName } = useAuth();
  const key = useScopedQueryKey();
  const namespace = namespaceName ?? '';
  return useQuery({
    queryKey: key('sites', 'options'),
    queryFn: ({ signal }) =>
      siteClient.listSites({ namespace, pageSize: OPTIONS_PAGE_SIZE, pageToken: '' }, { signal }),
    enabled: namespace !== '',
    staleTime: OPTIONS_STALE_TIME_MS,
    select: (res) => res.sites,
  });
}

/** Endpoint groups of a site (all clients); disabled until a site is chosen. */
export function useEndpointGroupOptions(site: string) {
  const { namespaceName } = useAuth();
  const key = useScopedQueryKey();
  const namespace = namespaceName ?? '';
  return useQuery({
    queryKey: key('sites', 'endpoint-group-options', site),
    queryFn: ({ signal }) =>
      siteClient.listEndpointGroups(
        { namespace, site, client: '', pageSize: OPTIONS_PAGE_SIZE, pageToken: '' },
        { signal },
      ),
    enabled: namespace !== '' && site !== '',
    staleTime: OPTIONS_STALE_TIME_MS,
    select: (res) => res.endpointGroups,
  });
}
