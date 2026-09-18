import { useQuery } from '@tanstack/react-query';

import { useAuth, useScopedPlaceholder, useScopedQueryKey } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';
import { type CursorPagination } from '@/components/data-table';
import { secretAdminClient } from '@/lib/clients';

/** Polling interval of the KEK status while a rewrap job runs. */
export const KEK_POLL_MS = 2000;
/** Paths loaded for the folder tree (one page, metadata only). */
export const FOLDER_TREE_PAGE_SIZE = 500;

export interface SecretFilters {
  search: string;
  tags: readonly string[];
}

/** Page of secret metadata. */
export function useSecrets(filters: SecretFilters, pager: CursorPagination) {
  const { namespaceName, can } = useAuth();
  const key = useScopedQueryKey();
  const keepPrevious = useScopedPlaceholder();
  return useQuery({
    queryKey: key('secrets', 'list', filters, pager.pageToken, pager.pageSize),
    queryFn: ({ signal }) =>
      secretAdminClient.listSecrets(
        {
          namespace: namespaceName ?? '',
          search: filters.search,
          tags: [...filters.tags],
          pageSize: pager.pageSize,
          pageToken: pager.pageToken,
        },
        { signal },
      ),
    enabled: Boolean(namespaceName) && can(PERMISSIONS.secretList),
    placeholderData: keepPrevious,
  });
}

/** First page of all secret paths for the folder tree. */
export function useSecretPaths() {
  const { namespaceName, can } = useAuth();
  const key = useScopedQueryKey();
  return useQuery({
    queryKey: key('secrets', 'paths'),
    queryFn: async ({ signal }) => {
      const res = await secretAdminClient.listSecrets(
        { namespace: namespaceName ?? '', pageSize: FOLDER_TREE_PAGE_SIZE },
        { signal },
      );
      return { paths: res.secrets.map((s) => s.path), truncated: res.nextPageToken !== '' };
    },
    enabled: Boolean(namespaceName) && can(PERMISSIONS.secretList),
  });
}

/** Secret metadata by ID (detail sheet). */
export function useSecret(id: string | undefined) {
  const key = useScopedQueryKey();
  return useQuery({
    queryKey: key('secrets', 'detail', id),
    queryFn: async ({ signal }) => {
      const res = await secretAdminClient.getSecret({ id: id ?? '' }, { signal });
      return res.secret ?? null;
    },
    enabled: Boolean(id),
  });
}

/** Page of version metadata of a secret. */
export function useSecretVersions(id: string, pager: CursorPagination, enabled = true) {
  const key = useScopedQueryKey();
  const keepPrevious = useScopedPlaceholder();
  return useQuery({
    queryKey: key('secrets', 'versions', id, pager.pageToken, pager.pageSize),
    queryFn: ({ signal }) =>
      secretAdminClient.listSecretVersions(
        { id, pageSize: pager.pageSize, pageToken: pager.pageToken },
        { signal },
      ),
    enabled: enabled && id !== '',
    placeholderData: keepPrevious,
  });
}

/** Newest versions for the reveal version picker. */
export function useRecentSecretVersions(id: string, enabled: boolean) {
  const key = useScopedQueryKey();
  return useQuery({
    queryKey: key('secrets', 'versions', id, 'recent'),
    queryFn: ({ signal }) => secretAdminClient.listSecretVersions({ id, pageSize: 100 }, { signal }),
    enabled: enabled && id !== '',
  });
}

/** Page of audited accesses of a secret. */
export function useSecretAccessLogs(id: string, pager: CursorPagination, enabled: boolean) {
  const key = useScopedQueryKey();
  const keepPrevious = useScopedPlaceholder();
  return useQuery({
    queryKey: key('secrets', 'access-logs', id, pager.pageToken, pager.pageSize),
    queryFn: ({ signal }) =>
      secretAdminClient.listSecretAccessLogs(
        { id, pageSize: pager.pageSize, pageToken: pager.pageToken },
        { signal },
      ),
    enabled: enabled && id !== '',
    placeholderData: keepPrevious,
  });
}

/** KEK configuration and rewrap progress (platform admins); polls while a job runs. */
export function useKekStatus(enabled: boolean) {
  const key = useScopedQueryKey();
  return useQuery({
    queryKey: key('secrets', 'kek-status'),
    queryFn: ({ signal }) => secretAdminClient.getKEKStatus({}, { signal }),
    enabled,
    refetchInterval: (query) => (query.state.data?.rewrapRunning ? KEK_POLL_MS : false),
  });
}
