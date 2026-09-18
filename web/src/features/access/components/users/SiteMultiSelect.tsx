import { ChevronDownIcon, RefreshCwIcon } from 'lucide-react';
import { useId, useState } from 'react';
import { useTranslation } from 'react-i18next';

import { TagsInput } from '@/components/TagsInput';
import { Button } from '@/components/ui/button';
import { Checkbox } from '@/components/ui/checkbox';
import { Input } from '@/components/ui/input';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';
import { Skeleton } from '@/components/ui/skeleton';
import { errorMessage } from '@/lib/errors';

import { useSiteNames } from '../../useSiteNames';

export interface SiteMultiSelectProps {
  /** Namespace whose sites are offered. */
  namespace: string;
  value: readonly string[];
  onChange: (sites: string[]) => void;
  disabled?: boolean;
  id?: string;
  'aria-invalid'?: boolean;
  'aria-describedby'?: string;
}

/**
 * Multi-select of site names of one namespace (SiteAdminService.ListSites).
 * When sites cannot be listed (e.g. missing site:read) names can be typed.
 */
export function SiteMultiSelect({ namespace, value, onChange, disabled, id, ...aria }: SiteMultiSelectProps) {
  const { t } = useTranslation('access');
  const listId = useId();
  const [filter, setFilter] = useState('');
  const sites = useSiteNames(namespace);

  if (sites.isError) {
    return (
      <div className="grid gap-1">
        <TagsInput
          id={id}
          value={value}
          onChange={onChange}
          disabled={disabled}
          placeholder={t('bindings.sitesManualPlaceholder')}
          {...aria}
        />
        <p className="flex items-center gap-1 text-xs text-muted-foreground">
          {t('bindings.sitesUnavailable', { reason: errorMessage(sites.error, t) })}
          <Button
            type="button"
            variant="link"
            size="sm"
            className="h-auto p-0 text-xs"
            onClick={() => void sites.refetch()}
          >
            <RefreshCwIcon className="size-3" />
            {t('common:actions.retry')}
          </Button>
        </p>
      </div>
    );
  }

  const names = sites.data ?? [];
  // Selected names missing from the list (renamed or deleted sites) stay visible.
  const options = [...value.filter((v) => !names.includes(v)), ...names];
  const needle = filter.trim().toLowerCase();
  const visible = needle ? options.filter((name) => name.toLowerCase().includes(needle)) : options;
  const toggle = (name: string, checked: boolean) =>
    onChange(checked ? [...value, name] : value.filter((v) => v !== name));

  return (
    <Popover>
      <PopoverTrigger asChild>
        <Button
          id={id}
          type="button"
          variant="outline"
          className="w-full justify-between font-normal"
          disabled={disabled}
          {...aria}
        >
          <span className="truncate">
            {value.length === 0
              ? t('bindings.allSites')
              : value.length <= 3
                ? value.join(', ')
                : t('bindings.sitesSelected', { count: value.length })}
          </span>
          <ChevronDownIcon className="opacity-50" />
        </Button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-(--radix-popover-trigger-width) min-w-64 p-2">
        <div className="grid gap-2">
          <Input
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            placeholder={t('bindings.filterSites')}
            aria-label={t('bindings.filterSites')}
            aria-controls={listId}
            className="h-8"
          />
          <div
            id={listId}
            role="group"
            aria-label={t('bindings.sites')}
            className="grid max-h-60 gap-0.5 overflow-y-auto"
          >
            {sites.isLoading ? (
              Array.from({ length: 4 }, (_, i) => <Skeleton key={i} className="h-7 w-full" />)
            ) : visible.length === 0 ? (
              <p className="px-2 py-3 text-center text-xs text-muted-foreground">
                {names.length === 0 ? t('bindings.noSites') : t('bindings.noMatchingSites')}
              </p>
            ) : (
              visible.map((name) => (
                <SiteOption
                  key={name}
                  name={name}
                  checked={value.includes(name)}
                  onCheckedChange={(checked) => toggle(name, checked)}
                />
              ))
            )}
          </div>
          <div className="flex items-center justify-between border-t pt-2 text-xs text-muted-foreground">
            <span>{t('bindings.sitesSelected', { count: value.length })}</span>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="h-7"
              disabled={value.length === 0}
              onClick={() => onChange([])}
            >
              {t('common:actions.clear')}
            </Button>
          </div>
        </div>
      </PopoverContent>
    </Popover>
  );
}

function SiteOption({
  name,
  checked,
  onCheckedChange,
}: {
  name: string;
  checked: boolean;
  onCheckedChange: (checked: boolean) => void;
}) {
  const id = useId();
  return (
    <div className="flex items-center gap-2 rounded-sm px-2 py-1 hover:bg-accent">
      <Checkbox id={id} checked={checked} onCheckedChange={(v) => onCheckedChange(v === true)} />
      <label htmlFor={id} className="flex-1 cursor-pointer truncate font-mono text-xs">
        {name}
      </label>
    </div>
  );
}
