import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';

import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { type IdentityType } from '@/gen/spinneret/v1/identity_admin_pb';
import { cn } from '@/lib/utils';

import { useIdentityTypeOptions } from '../useIdentityOptions';
import { ALL_VALUE } from './SiteSelect';

export interface IdentityTypeSelectProps {
  /** Site whose types are listed ("" = all accessible sites). */
  site: string;
  /** Identity type name. */
  value: string;
  onChange: (name: string, type: IdentityType | undefined) => void;
  allowAll?: boolean;
  disabled?: boolean;
  size?: 'sm' | 'default';
  className?: string;
  id?: string;
  'aria-invalid'?: boolean;
  'aria-describedby'?: string;
}

/** Identity type picker (names are unique per site; duplicates across sites are merged). */
export function IdentityTypeSelect({
  site,
  value,
  onChange,
  allowAll = false,
  disabled,
  size = 'default',
  className,
  ...aria
}: IdentityTypeSelectProps) {
  const { t } = useTranslation('identities');
  const types = useIdentityTypeOptions(site, allowAll || site !== '');
  const options = useMemo(() => {
    const byName = new Map<string, IdentityType>();
    for (const type of types.data ?? []) if (!byName.has(type.name)) byName.set(type.name, type);
    return [...byName.values()].sort((a, b) => a.name.localeCompare(b.name));
  }, [types.data]);
  const known = options.some((o) => o.name === value);

  return (
    <Select
      id={aria.id}
      aria-invalid={aria['aria-invalid']}
      aria-describedby={aria['aria-describedby']}
      value={value === '' ? (allowAll ? ALL_VALUE : '') : value}
      onValueChange={(v) => {
        const name = v === ALL_VALUE ? '' : v;
        onChange(
          name,
          options.find((o) => o.name === name),
        );
      }}
      disabled={disabled}
    >
      <SelectTrigger
        size={size}
        className={cn(size === 'sm' ? 'w-44' : 'w-full', className)}
        aria-label={t('fields.type')}
      >
        <SelectValue
          placeholder={
            types.isLoading
              ? t('common:table.loading')
              : types.isError
                ? t('fields.typesUnavailable')
                : t('fields.selectType')
          }
        />
      </SelectTrigger>
      <SelectContent>
        {allowAll && <SelectItem value={ALL_VALUE}>{t('filters.allTypes')}</SelectItem>}
        {value !== '' && !known && <SelectItem value={value}>{value}</SelectItem>}
        {options.map((type) => (
          <SelectItem key={type.id} value={type.name}>
            <span className="font-mono text-xs">{type.name}</span>
            <span className="text-muted-foreground">
              {site ? type.client : `${type.site} · ${type.client}`}
            </span>
          </SelectItem>
        ))}
        {!allowAll && options.length === 0 && !types.isLoading && (
          <div className="px-2 py-1.5 text-xs text-muted-foreground">{t('fields.noTypes')}</div>
        )}
      </SelectContent>
    </Select>
  );
}
