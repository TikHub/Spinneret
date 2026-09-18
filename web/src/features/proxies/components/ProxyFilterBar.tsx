import { MapPinIcon, RefreshCwIcon } from 'lucide-react';
import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';

import { FilterBar } from '@/components/FilterBar';
import { TagsInput } from '@/components/TagsInput';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { FormField } from '@/components/ui/form';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';
import { cn } from '@/lib/utils';

import {
  countActiveFilters,
  EMPTY_PROXY_FILTERS,
  FILTER_LIMITS,
  PROXY_KINDS,
  PROXY_STATES,
  type ProxyFilters,
} from '../proxyFilters';
import { DEFAULT_PROVIDER_RANGE } from '../providerStats';
import { useProviderStats } from '../useProxies';
import { MultiSelectFilter } from './MultiSelectFilter';

export interface ProxyFilterBarProps {
  filters: ProxyFilters;
  onChange: (filters: ProxyFilters) => void;
  onRefresh: () => void;
  refreshing: boolean;
}

const isValidListItem = (max: number) => (value: string) => value.length <= max;

/** Filters of the proxies table: search, state, kind, provider, region and tags. */
export function ProxyFilterBar({ filters, onChange, onRefresh, refreshing }: ProxyFilterBarProps) {
  const { t } = useTranslation('proxies');
  const [loadProviders, setLoadProviders] = useState(false);
  const providerStats = useProviderStats(DEFAULT_PROVIDER_RANGE, loadProviders);

  const stateOptions = useMemo(
    () => PROXY_STATES.map((state) => ({ value: state, label: t(`common:states.${state}`) })),
    [t],
  );
  const kindOptions = useMemo(
    () => PROXY_KINDS.map((kind) => ({ value: kind, label: t(`kinds.${kind}`) })),
    [t],
  );
  const providerOptions = useMemo(
    () =>
      (providerStats.data?.providers ?? [])
        .filter((p) => p.provider !== '')
        .map((p) => ({ value: p.provider, label: p.provider })),
    [providerStats.data],
  );

  const set = <K extends keyof ProxyFilters>(field: K, value: ProxyFilters[K]) =>
    onChange({ ...filters, [field]: value });
  const locationCount = filters.regions.length + filters.tags.length;

  return (
    <FilterBar
      search={{
        value: filters.search,
        onChange: (search) => set('search', search),
        placeholder: t('filters.search'),
      }}
      activeCount={countActiveFilters(filters)}
      onReset={() => onChange(EMPTY_PROXY_FILTERS)}
      actions={
        <Button variant="outline" size="icon-sm" onClick={onRefresh} aria-label={t('common:actions.refresh')}>
          <RefreshCwIcon className={cn(refreshing && 'animate-spin')} />
        </Button>
      }
    >
      <MultiSelectFilter
        label={t('filters.states')}
        options={stateOptions}
        value={filters.states}
        onChange={(states) => set('states', states)}
      />
      <MultiSelectFilter
        label={t('filters.kinds')}
        options={kindOptions}
        value={filters.kinds}
        onChange={(kinds) => set('kinds', kinds)}
      />
      <MultiSelectFilter
        label={t('filters.providers')}
        options={providerOptions}
        value={filters.providers}
        onChange={(providers) => set('providers', providers)}
        onOpenChange={(open) => open && setLoadProviders(true)}
        emptyText={providerStats.isLoading ? t('common:table.loading') : t('filters.noProviders')}
        allowCustom
        customPlaceholder={t('filters.providerPlaceholder')}
        maxItems={FILTER_LIMITS.providers}
      />
      <Popover>
        <PopoverTrigger asChild>
          <Button variant="outline" size="sm" className={cn('h-8', locationCount === 0 && 'border-dashed')}>
            <MapPinIcon />
            {t('filters.more')}
            {locationCount > 0 && <Badge variant="secondary">{locationCount}</Badge>}
          </Button>
        </PopoverTrigger>
        <PopoverContent align="start" className="grid w-80 gap-3">
          <FormField label={t('filters.regions')}>
            <TagsInput
              value={filters.regions}
              onChange={(regions) => set('regions', regions)}
              placeholder={t('filters.regionsPlaceholder')}
              maxTags={FILTER_LIMITS.regions}
              validate={isValidListItem(FILTER_LIMITS.regionLength)}
            />
          </FormField>
          <FormField label={t('filters.tags')} description={t('filters.tagsHint')}>
            <TagsInput
              value={filters.tags}
              onChange={(tags) => set('tags', tags)}
              placeholder={t('filters.tagsPlaceholder')}
              maxTags={FILTER_LIMITS.tags}
              validate={isValidListItem(FILTER_LIMITS.tagLength)}
            />
          </FormField>
        </PopoverContent>
      </Popover>
    </FilterBar>
  );
}
