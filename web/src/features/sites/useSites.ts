import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useCallback } from 'react';

import { useAuth, useScopedPlaceholder, useScopedQueryKey } from '@/app/auth/AuthContext';
import { LIVE_REFETCH_MS } from '@/app/queryClient';
import { type CursorPagination } from '@/components/data-table';
import { type URIRule } from '@/gen/spinneret/v1/site_admin_pb';
import { breakerClient, siteClient } from '@/lib/clients';

import {
  buildCreateGroupRequest,
  buildCreateSiteRequest,
  buildUpdateGroupRequest,
  buildUpdateSiteRequest,
} from './siteForm';
import { MAX_RULES, toRuleInputs, type RuleDraft } from './uriRules';

/** Upper bound of rule pages fetched for one group (MaxRulesPerGroup = 1000 at 500 per page). */
const MAX_RULE_PAGES = 10;

/** Invalidates site data and the domains that show site state. */
export function useInvalidateSites() {
  const queryClient = useQueryClient();
  return useCallback(
    (...extra: Array<'breakers' | 'dashboard' | 'identities' | 'policies'>) => {
      void queryClient.invalidateQueries({ queryKey: ['sites'] });
      for (const domain of extra) void queryClient.invalidateQueries({ queryKey: [domain] });
    },
    [queryClient],
  );
}

/** Paged site list; refreshes every 5 s unless paused. */
export function useSiteList(pager: CursorPagination, paused: boolean) {
  const { namespaceName } = useAuth();
  const key = useScopedQueryKey();
  const keepPrevious = useScopedPlaceholder();
  return useQuery({
    queryKey: key('sites', 'list', pager.pageToken, pager.pageSize),
    queryFn: ({ signal }) =>
      siteClient.listSites(
        { namespace: namespaceName ?? '', pageSize: pager.pageSize, pageToken: pager.pageToken },
        { signal },
      ),
    enabled: Boolean(namespaceName),
    placeholderData: keepPrevious,
    refetchInterval: paused ? false : LIVE_REFETCH_MS,
  });
}

/** One site by name (it may not be on the current list page). */
export function useSite(name: string | undefined, paused: boolean) {
  const { namespaceName } = useAuth();
  const key = useScopedQueryKey();
  return useQuery({
    queryKey: key('sites', 'detail', name),
    queryFn: ({ signal }) =>
      siteClient.getSite({ namespace: namespaceName ?? '', name: name ?? '' }, { signal }),
    enabled: Boolean(namespaceName) && Boolean(name),
    refetchInterval: paused ? false : LIVE_REFETCH_MS,
  });
}

/** Endpoint groups of one site client (live breaker state and availability). */
export function useEndpointGroups(site: string, client: string, pager: CursorPagination, paused: boolean) {
  const { namespaceName } = useAuth();
  const key = useScopedQueryKey();
  const keepPrevious = useScopedPlaceholder();
  return useQuery({
    queryKey: key('sites', 'groups', site, client, pager.pageToken, pager.pageSize),
    queryFn: ({ signal }) =>
      siteClient.listEndpointGroups(
        {
          namespace: namespaceName ?? '',
          site,
          client,
          pageSize: pager.pageSize,
          pageToken: pager.pageToken,
        },
        { signal },
      ),
    enabled: Boolean(namespaceName) && site !== '' && client !== '',
    placeholderData: keepPrevious,
    refetchInterval: paused ? false : LIVE_REFETCH_MS,
  });
}

/**
 * All URI rules of an endpoint group (follows page tokens). Not auto-refreshed: it backs an editor,
 * which only adopts rules fetched after it opened. The list is not kept once the editor closes
 * (gcTime 0): a later editor fetches it again anyway.
 */
export function useURIRules(groupId: string | undefined) {
  const key = useScopedQueryKey();
  return useQuery({
    queryKey: key('sites', 'rules', groupId),
    queryFn: async ({ signal }) => {
      const rules: URIRule[] = [];
      let pageToken = '';
      for (let page = 0; page < MAX_RULE_PAGES; page += 1) {
        const res = await siteClient.listURIRules(
          { endpointGroupId: groupId ?? '', pageSize: MAX_RULES, pageToken },
          { signal },
        );
        rules.push(...res.rules);
        pageToken = res.nextPageToken;
        if (!pageToken) break;
      }
      return rules;
    },
    enabled: Boolean(groupId),
    staleTime: 0,
    gcTime: 0,
    refetchOnWindowFocus: false,
  });
}

export function useCreateSite() {
  const { namespaceName } = useAuth();
  const invalidate = useInvalidateSites();
  return useMutation({
    mutationFn: (form: Parameters<typeof buildCreateSiteRequest>[1]) =>
      siteClient.createSite(buildCreateSiteRequest(namespaceName ?? '', form)),
    onSuccess: () => invalidate('dashboard'),
  });
}

export function useUpdateSite() {
  const invalidate = useInvalidateSites();
  return useMutation({
    mutationFn: (args: Parameters<typeof buildUpdateSiteRequest>) =>
      siteClient.updateSite(buildUpdateSiteRequest(...args)),
    onSuccess: () => invalidate('dashboard', 'breakers'),
  });
}

export function useDeleteSite() {
  const invalidate = useInvalidateSites();
  return useMutation({
    mutationFn: ({ id, force }: { id: string; force: boolean }) => siteClient.deleteSite({ id, force }),
    onSuccess: () => invalidate('dashboard', 'breakers', 'identities', 'policies'),
  });
}

export function useSetSitePaused() {
  const { namespaceName } = useAuth();
  const invalidate = useInvalidateSites();
  return useMutation({
    mutationFn: ({ site, paused, reason }: { site: string; paused: boolean; reason: string }) =>
      breakerClient.setSitePaused({ namespace: namespaceName ?? '', site, paused, reason }),
    onSuccess: () => invalidate('dashboard', 'breakers'),
  });
}

export function useCreateEndpointGroup() {
  const { namespaceName } = useAuth();
  const invalidate = useInvalidateSites();
  return useMutation({
    mutationFn: ({ site, form }: { site: string; form: Parameters<typeof buildCreateGroupRequest>[2] }) =>
      siteClient.createEndpointGroup(buildCreateGroupRequest(namespaceName ?? '', site, form)),
    onSuccess: () => invalidate('breakers'),
  });
}

export function useUpdateEndpointGroup() {
  const invalidate = useInvalidateSites();
  return useMutation({
    mutationFn: (args: Parameters<typeof buildUpdateGroupRequest>) =>
      siteClient.updateEndpointGroup(buildUpdateGroupRequest(...args)),
    onSuccess: () => invalidate('dashboard'),
  });
}

export function useDeleteEndpointGroup() {
  const invalidate = useInvalidateSites();
  return useMutation({
    mutationFn: (id: string) => siteClient.deleteEndpointGroup({ id }),
    onSuccess: () => invalidate('breakers', 'dashboard', 'policies'),
  });
}

export function useReplaceURIRules() {
  const invalidate = useInvalidateSites();
  return useMutation({
    mutationFn: ({ groupId, rules }: { groupId: string; rules: readonly RuleDraft[] }) =>
      siteClient.replaceURIRules({ endpointGroupId: groupId, rules: toRuleInputs(rules) }),
    onSuccess: () => invalidate(),
  });
}

export function useTestURI() {
  const { namespaceName } = useAuth();
  return useMutation({
    mutationFn: ({ site, client, uri }: { site: string; client: string; uri: string }) =>
      siteClient.testURI({ namespace: namespaceName ?? '', site, client, uri }),
  });
}
