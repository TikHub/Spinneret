import { useQuery } from '@tanstack/react-query';

import { useAuth, useScopedPlaceholder, useScopedQueryKey } from '@/app/auth/AuthContext';
import { type CursorPagination } from '@/components/data-table';
import { policyClient, siteClient } from '@/lib/clients';

/** Page size of the policy list (policies per namespace are few). */
export const POLICY_LIST_PAGE_SIZE = 200;
/** Page size of option lists (sites, endpoint groups, policies in pickers). */
export const OPTIONS_PAGE_SIZE = 500;

/** Policies of the active namespace (all kinds) for the grouped list. */
export function usePolicyList(search: string, pager: CursorPagination) {
  const { namespaceName } = useAuth();
  const key = useScopedQueryKey();
  const keepPrevious = useScopedPlaceholder();
  return useQuery({
    queryKey: key('policies', 'list', { search }, pager.pageToken, pager.pageSize),
    queryFn: ({ signal }) =>
      policyClient.listPolicies(
        { namespace: namespaceName ?? '', search, pageSize: pager.pageSize, pageToken: pager.pageToken },
        { signal },
      ),
    enabled: Boolean(namespaceName),
    placeholderData: keepPrevious,
  });
}

/** Policies of one kind (pickers: extends, bindings). */
export function usePolicyOptions(kind: string, enabled = true) {
  const { namespaceName } = useAuth();
  const key = useScopedQueryKey();
  return useQuery({
    queryKey: key('policies', 'options', kind),
    queryFn: ({ signal }) =>
      policyClient.listPolicies(
        { namespace: namespaceName ?? '', kind, pageSize: OPTIONS_PAGE_SIZE },
        { signal },
      ),
    enabled: Boolean(namespaceName) && enabled,
  });
}

export function usePolicy(id: string | undefined) {
  const { namespaceName } = useAuth();
  const key = useScopedQueryKey();
  return useQuery({
    queryKey: key('policies', 'detail', id),
    queryFn: ({ signal }) => policyClient.getPolicy({ id: id ?? '' }, { signal }),
    enabled: Boolean(namespaceName && id),
  });
}

export function usePolicyVersions(id: string, pager: CursorPagination) {
  const key = useScopedQueryKey();
  const keepPrevious = useScopedPlaceholder();
  return useQuery({
    queryKey: key('policies', 'versions', id, pager.pageToken, pager.pageSize),
    queryFn: ({ signal }) =>
      policyClient.listPolicyVersions(
        { id, pageSize: pager.pageSize, pageToken: pager.pageToken },
        { signal },
      ),
    enabled: id !== '',
    placeholderData: keepPrevious,
  });
}

export interface DiffSides {
  from: number;
  to: number;
}

export function usePolicyDiff(id: string, sides: DiffSides | undefined) {
  const key = useScopedQueryKey();
  const keepPrevious = useScopedPlaceholder();
  return useQuery({
    queryKey: key('policies', 'diff', id, sides?.from, sides?.to),
    queryFn: ({ signal }) =>
      policyClient.diffPolicyVersions(
        { id, fromVersion: sides?.from ?? 0, toVersion: sides?.to ?? 0 },
        { signal },
      ),
    enabled: id !== '' && sides !== undefined,
    placeholderData: keepPrevious,
  });
}

export interface BindingFilters {
  kind: string;
  site: string;
}

export function useBindings(filters: BindingFilters) {
  const { namespaceName } = useAuth();
  const key = useScopedQueryKey();
  const keepPrevious = useScopedPlaceholder();
  return useQuery({
    queryKey: key('policies', 'bindings', filters),
    queryFn: ({ signal }) =>
      policyClient.listBindings({ namespace: namespaceName ?? '', ...filters }, { signal }),
    enabled: Boolean(namespaceName),
    placeholderData: keepPrevious,
  });
}

export interface ScopeSelection {
  site: string;
  client: string;
  endpointGroup: string;
}

export function useResolvedPolicies(scope: ScopeSelection | undefined) {
  const { namespaceName } = useAuth();
  const key = useScopedQueryKey();
  return useQuery({
    queryKey: key('policies', 'resolve', scope),
    queryFn: ({ signal }) =>
      policyClient.resolvePolicies({ namespace: namespaceName ?? '', ...scope }, { signal }),
    enabled: Boolean(namespaceName) && scope !== undefined,
  });
}

/** Sites of the namespace for cascading scope selects. */
export function useSiteOptions() {
  const { namespaceName } = useAuth();
  const key = useScopedQueryKey();
  return useQuery({
    queryKey: key('sites', 'policy-options'),
    queryFn: ({ signal }) =>
      siteClient.listSites({ namespace: namespaceName ?? '', pageSize: OPTIONS_PAGE_SIZE }, { signal }),
    enabled: Boolean(namespaceName),
    staleTime: 30_000,
  });
}

/** Endpoint groups of a site and client for cascading scope selects. */
export function useEndpointGroupOptions(site: string, client: string) {
  const { namespaceName } = useAuth();
  const key = useScopedQueryKey();
  return useQuery({
    queryKey: key('sites', 'policy-endpoint-groups', site, client),
    queryFn: ({ signal }) =>
      siteClient.listEndpointGroups(
        { namespace: namespaceName ?? '', site, client, pageSize: OPTIONS_PAGE_SIZE },
        { signal },
      ),
    enabled: Boolean(namespaceName) && site !== '' && client !== '',
    staleTime: 30_000,
  });
}
