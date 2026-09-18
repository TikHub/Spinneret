import { RefreshCwIcon } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';

import { useAuth } from '@/app/auth/AuthContext';
import { PaginationControls, useCursorPagination } from '@/components/data-table';
import { FilterBar } from '@/components/FilterBar';
import { Button } from '@/components/ui/button';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { DAY_MS, HOUR_MS } from '@/lib/time';

import { ALERT_KINDS, SEVERITIES } from '../channelConfig';
import { useAlertEvents, useSiteNames, type AlertFilters, type ListScope } from '../useNotificationsApi';
import { AlertEventsTable } from './AlertEventsTable';

const ANY = '__any';
const RANGES = [
  { id: '1h', ms: HOUR_MS },
  { id: '24h', ms: DAY_MS },
  { id: '7d', ms: 7 * DAY_MS },
  { id: '30d', ms: 30 * DAY_MS },
  { id: 'all', ms: 0 },
] as const;

const INITIAL_FILTERS: AlertFilters = { scope: 'all', kind: '', severity: '', site: '', rangeMs: DAY_MS };

/** Fired alerts with filters (scope, kind, severity, site, time range); refreshed every 5 s. */
export function AlertHistoryTab() {
  const { t } = useTranslation('notifications');
  const { tenantId, namespaceName } = useAuth();
  const [filters, setFilters] = useState<AlertFilters>(INITIAL_FILTERS);
  // Site names belong to one namespace: clear the site filter after a tenant or namespace switch.
  const scopeKey = `${tenantId ?? ''}/${namespaceName ?? ''}`;
  const [filterScope, setFilterScope] = useState(scopeKey);
  if (filterScope !== scopeKey) {
    setFilterScope(scopeKey);
    if (filters.site) setFilters((prev) => ({ ...prev, site: '' }));
  }
  const pager = useCursorPagination({ resetOn: [tenantId, namespaceName, filters] });
  const events = useAlertEvents(filters, pager, false);
  const sites = useSiteNames(namespaceName ?? '', filters.scope === 'namespace');
  const set = <K extends keyof AlertFilters>(field: K, value: AlertFilters[K]) =>
    setFilters((prev) => ({ ...prev, [field]: value }));

  const active =
    (filters.scope !== INITIAL_FILTERS.scope ? 1 : 0) +
    (filters.kind ? 1 : 0) +
    (filters.severity ? 1 : 0) +
    (filters.site ? 1 : 0) +
    (filters.rangeMs !== INITIAL_FILTERS.rangeMs ? 1 : 0);
  const rangeId = RANGES.find((r) => r.ms === filters.rangeMs)?.id ?? '24h';

  return (
    <div className="grid gap-2">
      <FilterBar
        activeCount={active}
        onReset={() => setFilters(INITIAL_FILTERS)}
        actions={
          <Button
            variant="outline"
            size="icon-sm"
            aria-label={t('common:actions.refresh')}
            onClick={() => void events.refetch()}
          >
            <RefreshCwIcon className={events.isFetching ? 'animate-spin' : undefined} />
          </Button>
        }
      >
        <Select
          value={filters.scope}
          onValueChange={(v) => setFilters((prev) => ({ ...prev, scope: v as ListScope, site: '' }))}
        >
          <SelectTrigger size="sm" className="w-52" aria-label={t('fields.scope')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">{t('scope.allAlerts')}</SelectItem>
            <SelectItem value="namespace" disabled={!namespaceName}>
              {t('scope.namespaceNamed', { namespace: namespaceName ?? '' })}
            </SelectItem>
          </SelectContent>
        </Select>
        <Select value={filters.kind || ANY} onValueChange={(v) => set('kind', v === ANY ? '' : v)}>
          <SelectTrigger size="sm" className="w-52" aria-label={t('alerts.kind')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={ANY}>{t('alerts.anyKind')}</SelectItem>
            {ALERT_KINDS.map((kind) => (
              <SelectItem key={kind} value={kind}>
                {t(`alertKinds.${kind}.label`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select value={filters.severity || ANY} onValueChange={(v) => set('severity', v === ANY ? '' : v)}>
          <SelectTrigger size="sm" className="w-36" aria-label={t('alerts.severity')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={ANY}>{t('alerts.anySeverity')}</SelectItem>
            {SEVERITIES.map((severity) => (
              <SelectItem key={severity} value={severity}>
                {t(`severities.${severity}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select
          value={filters.site || ANY}
          onValueChange={(v) => set('site', v === ANY ? '' : v)}
          disabled={filters.scope !== 'namespace' || !sites.data}
        >
          <SelectTrigger size="sm" className="w-40" aria-label={t('alerts.site')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={ANY}>
              {filters.scope === 'namespace' ? t('alerts.anySite') : t('alerts.siteNeedsNamespace')}
            </SelectItem>
            {(sites.data ?? []).map((site) => (
              <SelectItem key={site} value={site}>
                {site}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select
          value={rangeId}
          onValueChange={(v) => set('rangeMs', RANGES.find((r) => r.id === v)?.ms ?? DAY_MS)}
        >
          <SelectTrigger size="sm" className="w-40" aria-label={t('alerts.range')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {RANGES.map((range) => (
              <SelectItem key={range.id} value={range.id}>
                {t(`alerts.ranges.${range.id}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </FilterBar>
      <div className="overflow-hidden rounded-lg border bg-card">
        <AlertEventsTable
          events={events.data?.events}
          isLoading={events.isLoading}
          error={events.error}
          onRetry={() => void events.refetch()}
          filtered={active > 0}
        />
        <PaginationControls
          pager={pager}
          nextPageToken={events.data?.nextPageToken}
          rowCount={events.data?.events.length ?? 0}
          disabled={events.isLoading}
        />
      </div>
    </div>
  );
}
