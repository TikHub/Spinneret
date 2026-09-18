import { useTranslation } from 'react-i18next';

import { FilterBar, SearchInput } from '@/components/FilterBar';

import {
  countActiveRequestFilters,
  MAX_HTTP_STATUS,
  MAX_MIN_LATENCY_MS,
  resetRequestFilters,
  type RequestFilters,
} from '../requestFilters';
import { OutcomeBadge } from '../shared/EventCells';
import { IntFilterInput } from '../shared/IntFilterInput';
import { MultiSelectMenu } from '../shared/MultiSelectMenu';
import { isOutcome, OUTCOMES } from '../shared/outcomes';
import { EndpointGroupSelect, SiteSelect } from '../shared/SiteSelect';
import { TimeRangePicker } from '../shared/TimeRangePicker';

export interface RequestFiltersBarProps {
  filters: RequestFilters;
  onChange: (filters: RequestFilters) => void;
}

const OUTCOME_OPTIONS = OUTCOMES.map((outcome) => ({
  value: outcome,
  label: <OutcomeBadge outcome={outcome} />,
}));

/** Filter toolbar of the request explorer (every change is written to the URL). */
export function RequestFiltersBar({ filters, onChange }: RequestFiltersBarProps) {
  const { t } = useTranslation('requests');
  const set = (patch: Partial<RequestFilters>) => onChange({ ...filters, ...patch });

  return (
    <FilterBar
      activeCount={countActiveRequestFilters(filters)}
      onReset={() => onChange(resetRequestFilters(filters))}
    >
      <TimeRangePicker value={filters.range} onChange={(range) => set({ range })} />
      <SiteSelect allowAll value={filters.site} onChange={(site) => set({ site, group: '' })} />
      <EndpointGroupSelect site={filters.site} value={filters.group} onChange={(group) => set({ group })} />
      <MultiSelectMenu
        label={t('filters.outcomes')}
        emptyText={t('filters.allOutcomes')}
        options={OUTCOME_OPTIONS}
        value={filters.outcomes}
        onChange={(outcomes) => set({ outcomes: outcomes.filter(isOutcome) })}
      />
      <SearchInput
        className="sm:w-44"
        value={filters.identity}
        onChange={(identity) => set({ identity: identity.trim() })}
        placeholder={t('filters.identity')}
      />
      <SearchInput
        className="sm:w-40"
        value={filters.proxy}
        onChange={(proxy) => set({ proxy: proxy.trim() })}
        placeholder={t('filters.proxy')}
      />
      <SearchInput
        className="sm:w-40"
        value={filters.node}
        onChange={(node) => set({ node: node.trim() })}
        placeholder={t('filters.node')}
      />
      <IntFilterInput
        className="w-28"
        value={filters.status}
        onChange={(status) => set({ status })}
        min={0}
        max={MAX_HTTP_STATUS}
        label={t('filters.status')}
      />
      <IntFilterInput
        className="w-36"
        value={filters.minLatency > 0 ? filters.minLatency : undefined}
        onChange={(minLatency) => set({ minLatency: minLatency ?? 0 })}
        min={0}
        max={MAX_MIN_LATENCY_MS}
        label={t('filters.minLatency')}
      />
    </FilterBar>
  );
}
