import { ArrowDownWideNarrowIcon, ArrowUpNarrowWideIcon, RefreshCwIcon } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { FilterBar } from '@/components/FilterBar';
import { Button } from '@/components/ui/button';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { SimpleTooltip } from '@/components/ui/tooltip';

import {
  countActiveFilters,
  IDENTITY_ORDER_BY,
  resetFilters,
  type IdentityListParams,
  type IdentityOrderBy,
} from '../identitySearch';
import { IdentityTypeSelect } from './IdentityTypeSelect';
import { MoreFiltersPopover } from './MoreFiltersPopover';
import { SiteSelect } from './SiteSelect';
import { StatesFilter } from './StatesFilter';

export interface IdentityFilterBarProps {
  params: IdentityListParams;
  onChange: (params: IdentityListParams) => void;
  onRefresh: () => void;
  isFetching: boolean;
}

/** Filters and sort of the identities list (all synced to the URL by the page). */
export function IdentityFilterBar({ params, onChange, onRefresh, isFetching }: IdentityFilterBarProps) {
  const { t } = useTranslation('identities');
  const set = (patch: Partial<IdentityListParams>) => onChange({ ...params, ...patch });
  const orderBy: IdentityOrderBy = params.orderBy || 'created_at';

  return (
    <FilterBar
      search={{
        value: params.search,
        onChange: (search) => set({ search }),
        placeholder: t('filters.searchPlaceholder'),
      }}
      activeCount={countActiveFilters(params)}
      onReset={() => onChange(resetFilters(params))}
      actions={
        <SimpleTooltip content={t('autoRefresh', { seconds: 5 })}>
          <Button
            variant="outline"
            size="icon-sm"
            onClick={onRefresh}
            aria-label={t('common:actions.refresh')}
          >
            <RefreshCwIcon className={isFetching ? 'animate-spin' : undefined} />
          </Button>
        </SimpleTooltip>
      }
    >
      <SiteSelect
        size="sm"
        allowAll
        value={params.site}
        onChange={(site) => set({ site, type: site === params.site ? params.type : '' })}
      />
      <IdentityTypeSelect
        size="sm"
        allowAll
        site={params.site}
        value={params.type}
        onChange={(type) => set({ type })}
      />
      <StatesFilter value={params.states} onChange={(states) => set({ states })} />
      <MoreFiltersPopover value={params} onApply={(filters) => set(filters)} />
      <div className="flex items-center gap-1">
        <Select
          value={orderBy}
          onValueChange={(v) => set({ orderBy: v === 'created_at' ? '' : (v as IdentityOrderBy) })}
        >
          <SelectTrigger size="sm" className="w-44" aria-label={t('filters.sortBy')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {IDENTITY_ORDER_BY.map((key) => (
              <SelectItem key={key} value={key}>
                {t(`filters.orderBy.${key}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <SimpleTooltip content={params.descending ? t('filters.descending') : t('filters.ascending')}>
          <Button
            variant="outline"
            size="icon-sm"
            onClick={() => set({ descending: !params.descending })}
            aria-label={params.descending ? t('filters.descending') : t('filters.ascending')}
            aria-pressed={params.descending}
          >
            {params.descending ? <ArrowDownWideNarrowIcon /> : <ArrowUpNarrowWideIcon />}
          </Button>
        </SimpleTooltip>
      </div>
    </FilterBar>
  );
}
