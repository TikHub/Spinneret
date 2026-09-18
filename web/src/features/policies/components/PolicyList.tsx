import { ChevronLeftIcon, ChevronRightIcon, LinkIcon, PlusIcon } from 'lucide-react';
import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';

import { useAuth } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';
import { PermissionButton } from '@/app/auth/PermissionGate';
import { useCursorPagination } from '@/components/data-table';
import { EmptyState } from '@/components/EmptyState';
import { ErrorState } from '@/components/ErrorState';
import { SearchInput } from '@/components/FilterBar';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Skeleton } from '@/components/ui/skeleton';
import { type Policy } from '@/gen/spinneret/v1/policy_admin_pb';
import { cn } from '@/lib/utils';

import { type PolicyKind } from '../constants';
import { groupByKind, KIND_ICONS } from '../selectors';
import { POLICY_LIST_PAGE_SIZE, usePolicyList } from '../usePolicyQueries';

export interface PolicyListProps {
  selectedId: string | undefined;
  onSelect: (id: string) => void;
  onCreate: () => void;
}

/** Left-hand policy list grouped by kind (name, version, draft badge, binding count). */
export function PolicyList({ selectedId, onSelect, onCreate }: PolicyListProps) {
  const { t } = useTranslation('policies');
  const { tenantId, namespaceName } = useAuth();
  const [search, setSearch] = useState('');
  const pager = useCursorPagination({
    pageSize: POLICY_LIST_PAGE_SIZE,
    resetOn: [tenantId, namespaceName, search],
  });
  const list = usePolicyList(search, pager);
  const groups = useMemo(() => groupByKind(list.data?.policies ?? []), [list.data]);
  const total = list.data?.total ?? 0;

  return (
    <nav aria-label={t('list.label')} className="flex min-h-0 flex-col gap-2 rounded-lg border bg-card p-2">
      <SearchInput value={search} onChange={setSearch} placeholder={t('list.search')} />
      {list.isLoading ? (
        <div className="grid gap-1.5 p-1">
          {Array.from({ length: 8 }, (_, i) => (
            <Skeleton key={i} className="h-9 w-full" />
          ))}
        </div>
      ) : list.isError && !list.data ? (
        <ErrorState error={list.error} onRetry={() => void list.refetch()} compact />
      ) : total === 0 && (list.data?.policies.length ?? 0) === 0 ? (
        <EmptyState
          compact
          title={search ? t('list.noMatches') : t('list.empty')}
          description={search ? undefined : t('list.emptyDescription')}
          action={
            !search && (
              <PermissionButton permission={PERMISSIONS.policyWrite} size="sm" onClick={onCreate}>
                <PlusIcon />
                {t('actions.new')}
              </PermissionButton>
            )
          }
        />
      ) : (
        <div
          className={cn('grid min-h-0 content-start gap-3 overflow-y-auto', list.isFetching && 'opacity-80')}
        >
          {groups.map((group) => (
            <PolicyGroup
              key={group.kind}
              kind={group.kind}
              policies={group.policies}
              selectedId={selectedId}
              onSelect={onSelect}
            />
          ))}
        </div>
      )}
      {(pager.canPrevious || list.data?.nextPageToken) && (
        <div className="flex items-center justify-between border-t pt-2">
          <Button
            variant="ghost"
            size="icon-sm"
            disabled={!pager.canPrevious}
            onClick={pager.previous}
            aria-label={t('common:table.previousPage')}
          >
            <ChevronLeftIcon />
          </Button>
          <span className="text-xs text-muted-foreground tabular">
            {t('common:table.page', { page: pager.pageIndex + 1 })}
          </span>
          <Button
            variant="ghost"
            size="icon-sm"
            disabled={!list.data?.nextPageToken}
            onClick={() => pager.next(list.data?.nextPageToken)}
            aria-label={t('common:table.nextPage')}
          >
            <ChevronRightIcon />
          </Button>
        </div>
      )}
    </nav>
  );
}

interface PolicyGroupProps {
  kind: PolicyKind;
  policies: readonly Policy[];
  selectedId: string | undefined;
  onSelect: (id: string) => void;
}

function PolicyGroup({ kind, policies, selectedId, onSelect }: PolicyGroupProps) {
  const { t } = useTranslation('policies');
  const Icon = KIND_ICONS[kind];
  return (
    <section aria-labelledby={`policy-group-${kind}`}>
      <h2
        id={`policy-group-${kind}`}
        className="flex items-center gap-1.5 px-2 pb-1 text-xs font-semibold tracking-wide text-muted-foreground uppercase"
      >
        <Icon className="size-3.5" aria-hidden />
        {t(`kinds.${kind}`)}
        <span className="font-normal tabular">({policies.length})</span>
      </h2>
      {policies.length === 0 ? (
        <p className="px-2 py-1 text-xs text-muted-foreground">{t('list.noneOfKind')}</p>
      ) : (
        <ul className="grid gap-0.5">
          {policies.map((p) => {
            const selected = p.id === selectedId;
            return (
              <li key={p.id}>
                <button
                  type="button"
                  aria-current={selected ? 'true' : undefined}
                  onClick={() => onSelect(p.id)}
                  className={cn(
                    'flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm outline-none hover:bg-accent focus-visible:ring-[3px] focus-visible:ring-ring/50',
                    selected && 'bg-accent font-medium',
                  )}
                >
                  <span className="min-w-0 flex-1 truncate font-mono text-xs" title={p.description || p.name}>
                    {p.name}
                  </span>
                  {p.hasDraft && (
                    <Badge
                      variant="outline"
                      className="border-amber-500/40 px-1.5 text-amber-700 dark:text-amber-400"
                    >
                      {t('list.draft')}
                    </Badge>
                  )}
                  {p.currentVersion > 0 ? (
                    <span className="font-mono text-xs text-muted-foreground tabular">
                      v{p.currentVersion}
                    </span>
                  ) : (
                    <span className="text-xs text-muted-foreground">{t('list.unpublished')}</span>
                  )}
                  {p.bindings.length > 0 && (
                    <span
                      className="inline-flex items-center gap-0.5 text-xs text-muted-foreground tabular"
                      title={t('list.bindings', { count: p.bindings.length })}
                    >
                      <LinkIcon className="size-3" aria-hidden />
                      {p.bindings.length}
                      <span className="sr-only">{t('list.bindings', { count: p.bindings.length })}</span>
                    </span>
                  )}
                </button>
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}
