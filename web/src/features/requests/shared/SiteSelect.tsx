import { RefreshCwIcon } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { Button } from '@/components/ui/button';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { errorMessage } from '@/lib/errors';
import { cn } from '@/lib/utils';

import { useEndpointGroupOptions, useSiteOptions } from './useSiteOptions';

/** Select value standing for "no filter" (Radix items cannot use an empty value). */
export const ALL_VALUE = '__all__';

function fromSelectValue(value: string): string {
  return value === ALL_VALUE ? '' : value;
}

interface RetryProps {
  error: unknown;
  onRetry: () => void;
}

function RetryButton({ error, onRetry }: RetryProps) {
  const { t } = useTranslation('requests');
  return (
    <Button
      variant="ghost"
      size="icon-sm"
      onClick={onRetry}
      title={errorMessage(error, t)}
      aria-label={t('common:actions.retry')}
    >
      <RefreshCwIcon className="text-destructive" />
    </Button>
  );
}

export interface SiteSelectProps {
  /** Site name; empty = all sites (when allowAll). */
  value: string;
  onChange: (site: string) => void;
  allowAll?: boolean;
  className?: string;
  id?: string;
}

/** Site filter backed by SiteAdminService.ListSites. */
export function SiteSelect({ value, onChange, allowAll = false, className, id }: SiteSelectProps) {
  const { t } = useTranslation('requests');
  const sites = useSiteOptions();
  const names = (sites.data ?? []).map((site) => site.name);
  if (value && !names.includes(value)) names.unshift(value);
  const selectValue = value || (allowAll ? ALL_VALUE : '');

  return (
    <div className="flex items-center gap-1">
      <Select
        value={selectValue}
        onValueChange={(v) => onChange(fromSelectValue(v))}
        disabled={sites.isLoading}
      >
        <SelectTrigger id={id} size="sm" className={cn('w-44', className)} aria-label={t('shared.site')}>
          <SelectValue placeholder={sites.isLoading ? t('common:table.loading') : t('shared.selectSite')} />
        </SelectTrigger>
        <SelectContent>
          {allowAll && <SelectItem value={ALL_VALUE}>{t('shared.allSites')}</SelectItem>}
          {names.map((name) => {
            const site = sites.data?.find((s) => s.name === name);
            return (
              <SelectItem key={name} value={name}>
                {site?.displayName && site.displayName !== name ? `${site.displayName} (${name})` : name}
              </SelectItem>
            );
          })}
          {!sites.isLoading && names.length === 0 && (
            <div className="px-2 py-1.5 text-sm text-muted-foreground">{t('shared.noSites')}</div>
          )}
        </SelectContent>
      </Select>
      {sites.isError && <RetryButton error={sites.error} onRetry={() => void sites.refetch()} />}
    </div>
  );
}

export interface EndpointGroupSelectProps {
  /** Site name the groups belong to; the select is disabled without it. */
  site: string;
  /** Endpoint group ID; empty = all groups. */
  value: string;
  onChange: (endpointGroupId: string) => void;
  className?: string;
}

/** Endpoint group filter (by ID) backed by SiteAdminService.ListEndpointGroups. */
export function EndpointGroupSelect({ site, value, onChange, className }: EndpointGroupSelectProps) {
  const { t } = useTranslation('requests');
  const groups = useEndpointGroupOptions(site);
  const options = (groups.data ?? []).map((g) => ({ id: g.id, label: `${g.client}/${g.name}` }));
  if (value && !options.some((o) => o.id === value)) options.unshift({ id: value, label: value });

  return (
    <div className="flex items-center gap-1">
      <Select
        value={value || ALL_VALUE}
        onValueChange={(v) => onChange(fromSelectValue(v))}
        disabled={!site || groups.isLoading}
      >
        <SelectTrigger size="sm" className={cn('w-48', className)} aria-label={t('shared.endpointGroup')}>
          <SelectValue placeholder={t('shared.allGroups')} />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={ALL_VALUE}>
            {site ? t('shared.allGroups') : t('shared.groupNeedsSite')}
          </SelectItem>
          {options.map((option) => (
            <SelectItem key={option.id} value={option.id}>
              {option.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      {groups.isError && <RetryButton error={groups.error} onRetry={() => void groups.refetch()} />}
    </div>
  );
}
