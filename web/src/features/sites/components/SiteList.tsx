import { ChevronLeftIcon, ChevronRightIcon, FingerprintIcon, GlobeIcon, WaypointsIcon } from 'lucide-react';
import { useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';

import { type CursorPagination } from '@/components/data-table';
import { EmptyState } from '@/components/EmptyState';
import { ErrorState } from '@/components/ErrorState';
import { StateBadge } from '@/components/StateBadge';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Skeleton } from '@/components/ui/skeleton';
import { type Site } from '@/gen/spinneret/v1/site_admin_pb';
import { formatNumber } from '@/lib/format';
import { cn } from '@/lib/utils';

/** Show the local filter once the page has more sites than this. */
const FILTER_THRESHOLD = 8;
const VISIBLE_CLIENTS = 3;

export interface SiteListProps {
  sites: readonly Site[] | undefined;
  total: number | undefined;
  selected: string | undefined;
  onSelect: (name: string) => void;
  isLoading: boolean;
  error: unknown;
  onRetry: () => void;
  pager: CursorPagination;
  nextPageToken: string | undefined;
  /** Create action shown in the empty state. */
  createAction?: ReactNode;
}

function SiteListItem({ site, selected, onSelect }: { site: Site; selected: boolean; onSelect: () => void }) {
  const { t, i18n } = useTranslation('sites');
  const lng = i18n.language;
  return (
    <li>
      <button
        type="button"
        aria-current={selected ? 'true' : undefined}
        onClick={onSelect}
        className={cn(
          'grid w-full gap-1 rounded-md border-l-2 border-transparent px-3 py-2 text-left outline-none hover:bg-muted/60 focus-visible:ring-2 focus-visible:ring-ring',
          selected && 'border-primary bg-primary/5 hover:bg-primary/10',
        )}
      >
        <span className="flex items-center gap-2">
          <span className="min-w-0 flex-1 truncate text-sm font-medium">{site.displayName || site.name}</span>
          {site.paused && <StateBadge kind="site" state="paused" />}
        </span>
        <span className="truncate font-mono text-xs text-muted-foreground">{site.name}</span>
        <span className="flex flex-wrap items-center gap-1 text-xs text-muted-foreground">
          {site.clients.slice(0, VISIBLE_CLIENTS).map((client) => (
            <Badge key={client} variant="muted" className="px-1.5 py-0 font-mono text-[11px]">
              {client}
            </Badge>
          ))}
          {site.clients.length > VISIBLE_CLIENTS && <span>+{site.clients.length - VISIBLE_CLIENTS}</span>}
          <span className="ml-auto inline-flex items-center gap-2">
            <span className="inline-flex items-center gap-0.5" title={t('list.endpointGroups')}>
              <WaypointsIcon className="size-3" aria-hidden />
              <span className="sr-only">{t('list.endpointGroups')}</span>
              <span className="tabular">{formatNumber(site.endpointGroupCount, undefined, lng)}</span>
            </span>
            <span className="inline-flex items-center gap-0.5" title={t('list.identities')}>
              <FingerprintIcon className="size-3" aria-hidden />
              <span className="sr-only">{t('list.identities')}</span>
              <span className="tabular">{formatNumber(site.identityCount, undefined, lng)}</span>
            </span>
          </span>
        </span>
      </button>
    </li>
  );
}

/** Master list of sites with a local name filter and cursor paging. */
export function SiteList({
  sites,
  total,
  selected,
  onSelect,
  isLoading,
  error,
  onRetry,
  pager,
  nextPageToken,
  createAction,
}: SiteListProps) {
  const { t, i18n } = useTranslation('sites');
  const [filter, setFilter] = useState('');
  const needle = filter.trim().toLowerCase();
  const visible = (sites ?? []).filter(
    (s) => needle === '' || s.name.includes(needle) || s.displayName.toLowerCase().includes(needle),
  );

  return (
    <nav
      aria-label={t('list.title')}
      className="flex min-h-0 flex-col gap-2 rounded-lg border bg-card p-2 lg:sticky lg:top-4 lg:max-h-[var(--sticky-panel-height)] lg:overflow-hidden"
    >
      <div className="flex items-center justify-between px-1 pt-1">
        <h2 className="text-sm font-semibold">{t('list.title')}</h2>
        {total !== undefined && (
          <span className="tabular text-xs text-muted-foreground">
            {formatNumber(total, undefined, i18n.language)}
          </span>
        )}
      </div>
      {(sites?.length ?? 0) > FILTER_THRESHOLD && (
        <Input
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          placeholder={t('list.filter')}
          aria-label={t('list.filter')}
          className="h-8"
        />
      )}
      {isLoading && !sites ? (
        <div className="grid gap-2 p-1">
          {Array.from({ length: 5 }, (_, i) => (
            <Skeleton key={i} className="h-16 w-full" />
          ))}
        </div>
      ) : error && !sites ? (
        <ErrorState error={error} onRetry={onRetry} compact />
      ) : (sites?.length ?? 0) === 0 ? (
        <EmptyState
          compact
          icon={GlobeIcon}
          title={t('list.empty')}
          description={t('list.emptyDescription')}
          action={createAction}
        />
      ) : visible.length === 0 ? (
        <p className="px-2 py-4 text-center text-sm text-muted-foreground">{t('list.noMatch')}</p>
      ) : (
        <ul className="grid gap-0.5 lg:min-h-0 lg:flex-1 lg:overflow-y-auto">
          {visible.map((site) => (
            <SiteListItem
              key={site.id}
              site={site}
              selected={site.name === selected}
              onSelect={() => onSelect(site.name)}
            />
          ))}
        </ul>
      )}
      {(pager.canPrevious || nextPageToken) && (
        <div className="flex items-center justify-between border-t px-1 pt-2 text-xs text-muted-foreground">
          <Button
            variant="ghost"
            size="icon-sm"
            onClick={pager.previous}
            disabled={!pager.canPrevious}
            aria-label={t('common:table.previousPage')}
          >
            <ChevronLeftIcon />
          </Button>
          <span>{t('common:table.page', { page: pager.pageIndex + 1 })}</span>
          <Button
            variant="ghost"
            size="icon-sm"
            onClick={() => pager.next(nextPageToken)}
            disabled={!nextPageToken}
            aria-label={t('common:table.nextPage')}
          >
            <ChevronRightIcon />
          </Button>
        </div>
      )}
    </nav>
  );
}
