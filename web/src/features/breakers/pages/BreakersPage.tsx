import { useNavigate, useSearch } from '@tanstack/react-router';
import { RefreshCwIcon } from 'lucide-react';
import { useCallback, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';

import { useAuth } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';
import { RequirePermission } from '@/app/auth/PermissionGate';
import { LIVE_REFETCH_MS } from '@/app/queryClient';
import { useCursorPagination } from '@/components/data-table';
import { EmptyState } from '@/components/EmptyState';
import { FilterBar } from '@/components/FilterBar';
import { PageHeader } from '@/components/PageHeader';
import { PageIntro } from '@/components/PageIntro';
import { TimeAgo } from '@/components/TimeAgo';
import { Button } from '@/components/ui/button';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs';
import { type BreakerStatus } from '@/gen/spinneret/v1/breaker_admin_pb';
import { cn } from '@/lib/utils';

import { BreakerHistory } from '../components/BreakerHistory';
import { CloseBreakerDialog, OpenBreakerDialog } from '../components/BreakerDialogs';
import { BreakerTable } from '../components/BreakerTable';
import { SiteSwitches } from '../components/SiteSwitches';
import { BREAKER_STATES, useBreakerList, useSites, type BreakerFilters } from '../useBreakers';

const TABS = ['breakers', 'sites', 'history'] as const;
type Tab = (typeof TABS)[number];
const ALL = '__all__';

function parseStates(value: unknown): string[] {
  const raw = Array.isArray(value) ? value : typeof value === 'string' ? value.split(',') : [];
  return BREAKER_STATES.filter((s) => raw.includes(s));
}

function BreakersContent() {
  const { t } = useTranslation('breakers');
  const { tenantId, namespaceName } = useAuth();
  const search = useSearch({ from: '/_app/breakers' });
  const navigate = useNavigate({ from: '/breakers' });
  const tab: Tab = TABS.find((x) => x === search.tab) ?? 'breakers';
  const filters = useMemo<BreakerFilters>(
    () => ({
      site: typeof search.site === 'string' ? search.site : '',
      client: typeof search.client === 'string' ? search.client : '',
      states: parseStates(search.states),
    }),
    [search.site, search.client, search.states],
  );
  const [opening, setOpening] = useState<BreakerStatus | null>(null);
  const [closing, setClosing] = useState<BreakerStatus | null>(null);
  const dialogOpen = opening !== null || closing !== null;
  const pager = useCursorPagination({ resetOn: [tenantId, namespaceName, filters] });
  const list = useBreakerList(filters, pager, dialogOpen || tab !== 'breakers');
  const sites = useSites();

  const updateSearch = (patch: Record<string, string | undefined>) =>
    void navigate({ search: (prev) => ({ ...prev, ...patch }), replace: true });
  const onOpen = useCallback((b: BreakerStatus) => setOpening(b), []);
  const onClose = useCallback((b: BreakerStatus) => setClosing(b), []);

  if (!namespaceName) return <EmptyState title={t('noNamespace')} />;

  const site = sites.data?.sites.find((s) => s.name === filters.site);
  const siteNames = sites.data?.sites.map((s) => s.name) ?? [];
  const toggleState = (state: string) => {
    const next = filters.states.includes(state)
      ? filters.states.filter((s) => s !== state)
      : [...filters.states, state];
    updateSearch({ states: next.length > 0 ? next.join(',') : undefined });
  };
  const activeCount = (filters.site ? 1 : 0) + (filters.client ? 1 : 0) + (filters.states.length > 0 ? 1 : 0);

  const toolbar = (
    <FilterBar
      activeCount={activeCount}
      onReset={() => updateSearch({ site: undefined, client: undefined, states: undefined })}
      actions={
        <>
          {list.dataUpdatedAt > 0 && (
            <span
              className="text-xs text-muted-foreground"
              title={t('autoRefresh', { seconds: LIVE_REFETCH_MS / 1000 })}
            >
              {t('common:time.updated', { time: '' })}
              <TimeAgo value={list.dataUpdatedAt} past />
            </span>
          )}
          <Button
            variant="outline"
            size="icon-sm"
            onClick={() => void list.refetch()}
            aria-label={t('common:actions.refresh')}
          >
            <RefreshCwIcon className={list.isFetching ? 'animate-spin' : undefined} />
          </Button>
        </>
      }
    >
      <Select
        value={filters.site || ALL}
        onValueChange={(v) => updateSearch({ site: v === ALL ? undefined : v, client: undefined })}
      >
        <SelectTrigger size="sm" className="w-44" aria-label={t('filters.site')}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={ALL}>{t('filters.allSites')}</SelectItem>
          {siteNames.map((name) => (
            <SelectItem key={name} value={name}>
              {name}
            </SelectItem>
          ))}
          {filters.site && !siteNames.includes(filters.site) && (
            <SelectItem value={filters.site}>{filters.site}</SelectItem>
          )}
        </SelectContent>
      </Select>
      <Select
        value={filters.client || ALL}
        onValueChange={(v) => updateSearch({ client: v === ALL ? undefined : v })}
        disabled={!filters.site}
      >
        <SelectTrigger size="sm" className="w-36" aria-label={t('filters.client')}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={ALL}>{t('filters.allClients')}</SelectItem>
          {(site?.clients ?? []).map((c) => (
            <SelectItem key={c} value={c}>
              {c}
            </SelectItem>
          ))}
          {filters.client && !site?.clients.includes(filters.client) && (
            <SelectItem value={filters.client}>{filters.client}</SelectItem>
          )}
        </SelectContent>
      </Select>
      <div role="group" aria-label={t('filters.states')} className="inline-flex rounded-md border p-0.5">
        {BREAKER_STATES.map((state) => {
          const pressed = filters.states.includes(state);
          return (
            <button
              key={state}
              type="button"
              aria-pressed={pressed}
              onClick={() => toggleState(state)}
              className={cn(
                'rounded px-2.5 py-1 text-xs outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50',
                pressed ? 'bg-secondary font-medium' : 'text-muted-foreground hover:text-foreground',
              )}
            >
              {t(`common:states.${state}`)}
            </button>
          );
        })}
      </div>
    </FilterBar>
  );

  return (
    <>
      <PageHeader title={t('title')} description={t('description', { namespace: namespaceName })} />
      <PageIntro
        page="breakers"
        links={[
          { to: '/policies', labelKey: 'nav.policies' },
          { to: '/sites', labelKey: 'nav.sites' },
        ]}
      />
      <Tabs value={tab} onValueChange={(v) => updateSearch({ tab: v === 'breakers' ? undefined : v })}>
        <TabsList>
          {TABS.map((x) => (
            <TabsTrigger key={x} value={x}>
              {t(`tabs.${x}`)}
            </TabsTrigger>
          ))}
        </TabsList>
        <TabsContent value="breakers">
          <BreakerTable
            breakers={list.data?.breakers}
            isLoading={list.isLoading}
            isFetching={list.isFetching}
            error={list.error}
            onRetry={() => void list.refetch()}
            pagination={{ pager, nextPageToken: list.data?.nextPageToken, total: list.data?.total }}
            onOpen={onOpen}
            onClose={onClose}
            toolbar={toolbar}
            emptyDescription={activeCount > 0 ? t('emptyFiltered') : t('emptyDescription')}
          />
        </TabsContent>
        <TabsContent value="sites">
          <p className="mb-3 text-sm text-muted-foreground">{t('sites.description')}</p>
          <SiteSwitches paused={tab !== 'sites'} />
        </TabsContent>
        <TabsContent value="history">
          <BreakerHistory initialSite={filters.site} />
        </TabsContent>
      </Tabs>
      <OpenBreakerDialog breaker={opening} onOpenChange={(open) => !open && setOpening(null)} />
      <CloseBreakerDialog breaker={closing} onOpenChange={(open) => !open && setClosing(null)} />
    </>
  );
}

/** Breakers: live endpoint group breaker states, manual open/close, site switches and transition history. */
export default function BreakersPage() {
  return (
    <RequirePermission permission={PERMISSIONS.breakerRead}>
      <BreakersContent />
    </RequirePermission>
  );
}
