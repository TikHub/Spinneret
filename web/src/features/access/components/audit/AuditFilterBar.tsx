import { type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';

import { useAuth } from '@/app/auth/AuthContext';
import { FilterBar, SearchInput } from '@/components/FilterBar';
import { Input } from '@/components/ui/input';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';

import {
  activeAuditFilterCount,
  AUDIT_RANGE_PRESETS,
  AUDIT_RESULTS,
  DEFAULT_AUDIT_FILTERS,
  type AuditFilters,
  type AuditRange,
  type AuditResult,
} from '../../auditFilters';

/** Select value for "any" (Radix Select items cannot use ""). */
const ANY = '__any__';

export interface AuditFilterBarProps {
  filters: AuditFilters;
  onChange: (filters: AuditFilters) => void;
  /** True when the custom range end is not after its start. */
  rangeInvalid: boolean;
  actions?: ReactNode;
}

/** Audit log filters: namespace, actor, action, resource, result and time range. */
export function AuditFilterBar({ filters, onChange, rangeInvalid, actions }: AuditFilterBarProps) {
  const { t } = useTranslation('access');
  const { namespaces } = useAuth();
  const names = namespaces.map((access) => access.namespace?.name ?? '').filter(Boolean);
  const namespaceOptions =
    filters.namespace !== '' && !names.includes(filters.namespace) ? [filters.namespace, ...names] : names;
  const set = <K extends keyof AuditFilters>(field: K, value: AuditFilters[K]) =>
    onChange({ ...filters, [field]: value });

  return (
    <div className="grid gap-2">
      <FilterBar
        activeCount={activeAuditFilterCount(filters)}
        onReset={() => onChange(DEFAULT_AUDIT_FILTERS)}
        actions={actions}
      >
        <Select
          value={filters.namespace === '' ? ANY : filters.namespace}
          onValueChange={(value) => set('namespace', value === ANY ? '' : value)}
        >
          <SelectTrigger size="sm" className="w-40" aria-label={t('audit.filters.namespace')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={ANY}>{t('audit.filters.allNamespaces')}</SelectItem>
            {namespaceOptions.map((name) => (
              <SelectItem key={name} value={name} className="font-mono">
                {name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <SearchInput
          value={filters.actor}
          onChange={(value) => set('actor', value)}
          placeholder={t('audit.filters.actor')}
          className="sm:w-40"
        />
        <SearchInput
          value={filters.action}
          onChange={(value) => set('action', value)}
          placeholder={t('audit.filters.action')}
          className="sm:w-40"
        />
        <SearchInput
          value={filters.resourceKind}
          onChange={(value) => set('resourceKind', value)}
          placeholder={t('audit.filters.resourceKind')}
          className="sm:w-36"
        />
        <SearchInput
          value={filters.resourceId}
          onChange={(value) => set('resourceId', value)}
          placeholder={t('audit.filters.resourceId')}
          className="sm:w-40"
        />
        <Select
          value={filters.result === '' ? ANY : filters.result}
          onValueChange={(value) => set('result', value === ANY ? '' : (value as AuditResult))}
        >
          <SelectTrigger size="sm" className="w-32" aria-label={t('audit.filters.result')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={ANY}>{t('audit.filters.anyResult')}</SelectItem>
            {AUDIT_RESULTS.map((result) => (
              <SelectItem key={result} value={result}>
                {t(`audit.results.${result}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select value={filters.range} onValueChange={(value) => set('range', value as AuditRange)}>
          <SelectTrigger size="sm" className="w-36" aria-label={t('audit.filters.range')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {AUDIT_RANGE_PRESETS.map((range) => (
              <SelectItem key={range} value={range}>
                {t(`audit.ranges.${range}`)}
              </SelectItem>
            ))}
            <SelectItem value="custom">{t('audit.ranges.custom')}</SelectItem>
          </SelectContent>
        </Select>
      </FilterBar>
      {filters.range === 'custom' && (
        <div className="flex flex-wrap items-center gap-2 text-sm">
          <Input
            type="datetime-local"
            value={filters.from}
            onChange={(e) => set('from', e.target.value)}
            aria-label={t('audit.filters.from')}
            aria-invalid={rangeInvalid}
            className="h-8 w-52"
          />
          <span className="text-muted-foreground">–</span>
          <Input
            type="datetime-local"
            value={filters.to}
            onChange={(e) => set('to', e.target.value)}
            aria-label={t('audit.filters.to')}
            aria-invalid={rangeInvalid}
            className="h-8 w-52"
          />
          <span
            className={rangeInvalid ? 'text-xs text-destructive' : 'text-xs text-muted-foreground'}
            role={rangeInvalid ? 'alert' : undefined}
          >
            {rangeInvalid ? t('audit.filters.rangeInvalid') : t('audit.filters.rangeHint')}
          </span>
        </div>
      )}
    </div>
  );
}
