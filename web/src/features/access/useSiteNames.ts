import { useQuery } from '@tanstack/react-query';

import { useScopedQueryKey } from '@/app/auth/AuthContext';
import { siteClient } from '@/lib/clients';

/** Page size used while collecting site names. */
const SITE_PAGE_SIZE = 500;
/** Upper bound of site names offered in pickers. */
const MAX_SITE_NAMES = 5000;
const SITE_NAMES_STALE_MS = 30_000;

/** Lists every site name of a namespace (SiteAdminService.ListSites, all pages). */
export async function fetchSiteNames(namespace: string, signal?: AbortSignal): Promise<string[]> {
  const names: string[] = [];
  let pageToken = '';
  do {
    const res = await siteClient.listSites({ namespace, pageSize: SITE_PAGE_SIZE, pageToken }, { signal });
    names.push(...res.sites.map((site) => site.name));
    pageToken = res.nextPageToken;
  } while (pageToken !== '' && names.length < MAX_SITE_NAMES);
  return names;
}

/** Site names of a namespace for pickers (scope builder, role bindings). */
export function useSiteNames(namespace: string | undefined, enabled = true) {
  const key = useScopedQueryKey();
  return useQuery({
    queryKey: key('sites', 'names', namespace ?? ''),
    queryFn: ({ signal }) => fetchSiteNames(namespace ?? '', signal),
    enabled: enabled && Boolean(namespace),
    staleTime: SITE_NAMES_STALE_MS,
  });
}
