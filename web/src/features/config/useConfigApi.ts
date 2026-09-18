import { create } from '@bufbuild/protobuf';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useCallback } from 'react';

import { useAuth, useScopedPlaceholder, useScopedQueryKey } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';
import { type CursorPagination } from '@/components/data-table';
import { ConfigLocatorSchema, type ConfigItemInfo } from '@/gen/spinneret/v1/config_admin_pb';
import { configAdminClient } from '@/lib/clients';

/** Selected item address (group + key) inside the active namespace. */
export interface ConfigSelection {
  group: string;
  key: string;
}

/** Page of config items for the tree. */
export function useConfigItems(search: string, pager: CursorPagination) {
  const { namespaceName, can } = useAuth();
  const key = useScopedQueryKey();
  const keepPrevious = useScopedPlaceholder();
  return useQuery({
    queryKey: key('config', 'items', search, pager.pageToken, pager.pageSize),
    queryFn: ({ signal }) =>
      configAdminClient.listConfigItems(
        { namespace: namespaceName ?? '', search, pageSize: pager.pageSize, pageToken: pager.pageToken },
        { signal },
      ),
    enabled: Boolean(namespaceName) && can(PERMISSIONS.configRead),
    placeholderData: keepPrevious,
  });
}

/** One config item by group and key (works for "_runtime" items, which have no ID). */
export function useConfigItem(selection: ConfigSelection | undefined) {
  const { namespaceName } = useAuth();
  const key = useScopedQueryKey();
  return useQuery({
    queryKey: key('config', 'item', selection?.group, selection?.key),
    queryFn: async ({ signal }) => {
      const res = await configAdminClient.getConfigItem(
        {
          selector: {
            case: 'locator',
            value: create(ConfigLocatorSchema, {
              namespace: namespaceName ?? '',
              group: selection?.group ?? '',
              key: selection?.key ?? '',
            }),
          },
        },
        { signal },
      );
      return res.item ?? null;
    },
    enabled: Boolean(namespaceName) && selection !== undefined,
  });
}

/** Page of published versions of an item. */
export function useConfigVersions(itemId: string, pager: CursorPagination, enabled = true) {
  const key = useScopedQueryKey();
  const keepPrevious = useScopedPlaceholder();
  return useQuery({
    queryKey: key('config', 'versions', itemId, pager.pageToken, pager.pageSize),
    queryFn: ({ signal }) =>
      configAdminClient.listConfigVersions(
        { id: itemId, pageSize: pager.pageSize, pageToken: pager.pageToken },
        { signal },
      ),
    enabled: enabled && itemId !== '',
    placeholderData: keepPrevious,
  });
}

/** Newest versions of an item (rollback picker). */
export function useRecentConfigVersions(itemId: string, enabled: boolean, pageSize = 100) {
  const key = useScopedQueryKey();
  return useQuery({
    queryKey: key('config', 'versions', itemId, 'recent', pageSize),
    queryFn: ({ signal }) => configAdminClient.listConfigVersions({ id: itemId, pageSize }, { signal }),
    enabled: enabled && itemId !== '',
  });
}

/** Server diff between two versions (0 = current published / draft). */
export function useConfigDiff(itemId: string, fromVersion: number, toVersion: number, enabled: boolean) {
  const key = useScopedQueryKey();
  return useQuery({
    queryKey: key('config', 'diff', itemId, fromVersion, toVersion),
    queryFn: ({ signal }) =>
      configAdminClient.diffConfigVersions({ id: itemId, fromVersion, toVersion }, { signal }),
    enabled: enabled && itemId !== '',
  });
}

/** Stores a fresh item in the cache and invalidates config lists. */
export function useConfigCacheUpdate() {
  const queryClient = useQueryClient();
  const key = useScopedQueryKey();
  return useCallback(
    (item: ConfigItemInfo | undefined) => {
      if (item) queryClient.setQueryData(key('config', 'item', item.group, item.key), item);
      void queryClient.invalidateQueries({ queryKey: ['config'] });
    },
    [queryClient, key],
  );
}

/** Drops a deleted item from the cache (no refetch of a missing item) and refreshes lists. */
export function useConfigCacheRemove() {
  const queryClient = useQueryClient();
  const key = useScopedQueryKey();
  return useCallback(
    (selection: ConfigSelection) => {
      queryClient.removeQueries({ queryKey: key('config', 'item', selection.group, selection.key) });
      void queryClient.invalidateQueries({ queryKey: ['config', ...key('config').slice(1), 'items'] });
    },
    [queryClient, key],
  );
}
