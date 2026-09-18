import { useTranslation } from 'react-i18next';

import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { cn } from '@/lib/utils';

import { useEndpointGroupOptions } from '../useIdentityOptions';

export interface EndpointGroupSelectProps {
  site: string;
  /** Endpoint group ID. */
  value: string;
  onChange: (endpointGroupId: string) => void;
  disabled?: boolean;
  className?: string;
  id?: string;
  'aria-invalid'?: boolean;
  'aria-describedby'?: string;
}

/** Endpoint group picker for a site (client / name). */
export function EndpointGroupSelect({
  site,
  value,
  onChange,
  disabled,
  className,
  ...aria
}: EndpointGroupSelectProps) {
  const { t } = useTranslation('identities');
  const groups = useEndpointGroupOptions(site);
  const options = groups.data ?? [];
  const placeholder = !site
    ? t('fields.selectSiteFirst')
    : groups.isLoading
      ? t('common:table.loading')
      : groups.isError
        ? t('fields.groupsUnavailable')
        : options.length === 0
          ? t('fields.noGroups')
          : t('fields.selectGroup');

  return (
    <Select
      id={aria.id}
      aria-invalid={aria['aria-invalid']}
      aria-describedby={aria['aria-describedby']}
      value={value}
      onValueChange={onChange}
      disabled={disabled || !site || options.length === 0}
    >
      <SelectTrigger className={cn('w-full', className)} aria-label={t('fields.endpointGroup')}>
        <SelectValue placeholder={placeholder} />
      </SelectTrigger>
      <SelectContent>
        {options.map((group) => (
          <SelectItem key={group.id} value={group.id}>
            <span className="text-muted-foreground">{group.client} /</span>
            <span className="font-mono text-xs">{group.name}</span>
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}
