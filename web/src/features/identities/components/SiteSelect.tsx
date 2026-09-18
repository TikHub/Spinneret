import { useTranslation } from 'react-i18next';

import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { cn } from '@/lib/utils';

import { useSiteOptions } from '../useIdentityOptions';
import { DebouncedInput } from './DebouncedInput';

/** Radix Select items cannot use "" as a value. */
export const ALL_VALUE = '__all__';

export interface SiteSelectProps {
  /** Site name; "" = all sites (with allowAll) or none selected. */
  value: string;
  onChange: (site: string) => void;
  allowAll?: boolean;
  disabled?: boolean;
  size?: 'sm' | 'default';
  className?: string;
  placeholder?: string;
  id?: string;
  'aria-invalid'?: boolean;
  'aria-describedby'?: string;
  'aria-label'?: string;
}

/** Site picker backed by SiteAdminService.ListSites; falls back to a text input when sites cannot be listed. */
export function SiteSelect({
  value,
  onChange,
  allowAll = false,
  disabled,
  size = 'default',
  className,
  placeholder,
  ...aria
}: SiteSelectProps) {
  const { t } = useTranslation('identities');
  const sites = useSiteOptions();
  const label = aria['aria-label'] ?? t('fields.site');

  if (sites.isError) {
    return (
      <DebouncedInput
        id={aria.id}
        value={value}
        onChange={onChange}
        disabled={disabled}
        placeholder={placeholder ?? t('fields.sitePlaceholder')}
        aria-label={label}
        aria-invalid={aria['aria-invalid']}
        aria-describedby={aria['aria-describedby']}
        className={cn(size === 'sm' ? 'h-8 w-40' : 'w-full', className)}
      />
    );
  }

  const options = sites.data ?? [];
  const known = options.some((s) => s.name === value);
  return (
    <Select
      id={aria.id}
      aria-invalid={aria['aria-invalid']}
      aria-describedby={aria['aria-describedby']}
      value={value === '' ? (allowAll ? ALL_VALUE : '') : value}
      onValueChange={(v) => onChange(v === ALL_VALUE ? '' : v)}
      disabled={disabled}
    >
      <SelectTrigger
        size={size}
        className={cn(size === 'sm' ? 'w-40' : 'w-full', className)}
        aria-label={label}
      >
        <SelectValue
          placeholder={sites.isLoading ? t('common:table.loading') : (placeholder ?? t('fields.selectSite'))}
        />
      </SelectTrigger>
      <SelectContent>
        {allowAll && <SelectItem value={ALL_VALUE}>{t('filters.allSites')}</SelectItem>}
        {value !== '' && !known && <SelectItem value={value}>{value}</SelectItem>}
        {options.map((site) => (
          <SelectItem key={site.id} value={site.name}>
            {site.displayName && site.displayName !== site.name
              ? `${site.displayName} (${site.name})`
              : site.name}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}
