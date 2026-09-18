import { useTranslation } from 'react-i18next';

import { FilterBar, SearchInput } from '@/components/FilterBar';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { OutcomeBadge } from '@/features/requests/shared/EventCells';
import { RISK_EVENT_OUTCOMES } from '@/features/requests/shared/outcomes';
import { ALL_VALUE, EndpointGroupSelect, SiteSelect } from '@/features/requests/shared/SiteSelect';
import { TimeRangePicker } from '@/features/requests/shared/TimeRangePicker';

import { countActiveRiskFilters, DEFAULT_RISK_FILTERS, type RiskFilters } from '../riskFilters';

export interface RiskFiltersBarProps {
  filters: RiskFilters;
  onChange: (filters: RiskFilters) => void;
}

/** Filter toolbar of the risk event list (every change is written to the URL). */
export function RiskFiltersBar({ filters, onChange }: RiskFiltersBarProps) {
  const { t } = useTranslation('risk-events');
  const set = (patch: Partial<RiskFilters>) => onChange({ ...filters, ...patch });

  return (
    <FilterBar
      activeCount={countActiveRiskFilters(filters)}
      onReset={() => onChange({ ...DEFAULT_RISK_FILTERS, range: filters.range })}
    >
      <TimeRangePicker value={filters.range} onChange={(range) => set({ range })} />
      <SiteSelect allowAll value={filters.site} onChange={(site) => set({ site, group: '' })} />
      <EndpointGroupSelect site={filters.site} value={filters.group} onChange={(group) => set({ group })} />
      <Select
        value={filters.outcome || ALL_VALUE}
        onValueChange={(v) => set({ outcome: v === ALL_VALUE ? '' : v })}
      >
        <SelectTrigger size="sm" className="w-40" aria-label={t('filters.outcome')}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={ALL_VALUE}>{t('filters.allOutcomes')}</SelectItem>
          {RISK_EVENT_OUTCOMES.map((outcome) => (
            <SelectItem key={outcome} value={outcome}>
              <OutcomeBadge outcome={outcome} />
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
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
    </FilterBar>
  );
}
