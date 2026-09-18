import { useQuery } from '@tanstack/react-query';
import { useNavigate, useSearch } from '@tanstack/react-router';
import { RefreshCwIcon } from 'lucide-react';
import { useId, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';

import { useAuth, useScopedPlaceholder, useScopedQueryKey } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';
import { RequirePermission } from '@/app/auth/PermissionGate';
import { LIVE_REFETCH_MS } from '@/app/queryClient';
import { useCursorPagination } from '@/components/data-table';
import { PageHeader } from '@/components/PageHeader';
import { PageIntro } from '@/components/PageIntro';
import { Button } from '@/components/ui/button';
import { Label } from '@/components/ui/label';
import { Switch } from '@/components/ui/switch';
import { accessClient } from '@/lib/clients';

import {
  parseAuditSearch,
  resolveAuditTimeRange,
  toAuditSearch,
  toListAuditLogsInit,
  type AuditFilters,
} from '../auditFilters';
import { AuditFilterBar } from '../components/audit/AuditFilterBar';
import { AuditTable } from '../components/audit/AuditTable';

/** Audit log search with filters kept in the URL. */
export default function AuditPage() {
  return (
    <RequirePermission permission={PERMISSIONS.auditRead}>
      <AuditContent />
    </RequirePermission>
  );
}

function AuditContent() {
  const { t } = useTranslation('access');
  const { tenantId, namespaceName } = useAuth();
  const search = useSearch({ from: '/_app/access/audit' });
  const navigate = useNavigate({ from: '/access/audit' });
  const key = useScopedQueryKey();
  const keepPrevious = useScopedPlaceholder();
  const liveId = useId();
  const [live, setLive] = useState(true);

  const filters = useMemo(() => parseAuditSearch(search), [search]);
  const setFilters = (next: AuditFilters) => void navigate({ search: toAuditSearch(next), replace: true });
  // Only a custom range can be invalid, which does not depend on the current time.
  const rangeInvalid = resolveAuditTimeRange(filters, 0) === 'invalid';

  const pager = useCursorPagination({ resetOn: [tenantId, namespaceName, filters] });
  const query = useQuery({
    queryKey: key('access', 'audit', filters, pager.pageToken, pager.pageSize),
    queryFn: ({ signal }) =>
      accessClient.listAuditLogs(
        { ...toListAuditLogsInit(filters, Date.now()), pageSize: pager.pageSize, pageToken: pager.pageToken },
        { signal },
      ),
    enabled: !rangeInvalid,
    placeholderData: keepPrevious,
    // Tail new entries on the first page only; later pages stay stable while reading.
    refetchInterval: live && pager.pageIndex === 0 ? LIVE_REFETCH_MS : false,
  });

  return (
    <>
      <PageHeader title={t('audit.title')} description={t('audit.description')} />
      <PageIntro page="audit" links={[{ to: '/access/users', labelKey: 'nav.users' }]} />
      <div className="grid gap-3">
        <AuditFilterBar
          filters={filters}
          onChange={setFilters}
          rangeInvalid={rangeInvalid}
          actions={
            <>
              <div className="flex items-center gap-2">
                <Switch id={liveId} checked={live} onCheckedChange={setLive} />
                <Label htmlFor={liveId} className="text-sm font-normal">
                  {t('audit.live')}
                </Label>
              </div>
              <Button
                variant="outline"
                size="icon-sm"
                onClick={() => void query.refetch()}
                disabled={rangeInvalid}
                aria-label={t('common:actions.refresh')}
              >
                <RefreshCwIcon className={query.isFetching ? 'animate-spin' : undefined} />
              </Button>
            </>
          }
        />
        <AuditTable
          logs={rangeInvalid ? [] : query.data?.logs}
          isLoading={query.isLoading}
          error={query.error}
          onRetry={() => void query.refetch()}
          pagination={{ pager, nextPageToken: query.data?.nextPageToken }}
        />
      </div>
    </>
  );
}
