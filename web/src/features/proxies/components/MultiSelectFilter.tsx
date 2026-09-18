import { ListFilterIcon } from 'lucide-react';
import { useId } from 'react';
import { useTranslation } from 'react-i18next';

import { TagsInput } from '@/components/TagsInput';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Checkbox } from '@/components/ui/checkbox';
import { FormField } from '@/components/ui/form';
import { Label } from '@/components/ui/label';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';
import { cn } from '@/lib/utils';

export interface FilterOption {
  value: string;
  label: string;
}

export interface MultiSelectFilterProps {
  label: string;
  options: readonly FilterOption[];
  value: readonly string[];
  onChange: (value: string[]) => void;
  /** Adds a free-text input for values missing from the options. */
  allowCustom?: boolean;
  customPlaceholder?: string;
  maxItems?: number;
  /** Called when the popover opens or closes (e.g. to load options lazily). */
  onOpenChange?: (open: boolean) => void;
  /** Shown instead of the option list while options load or when there are none. */
  emptyText?: string;
}

/** Toolbar filter selecting several values from a list (checkboxes in a popover). */
export function MultiSelectFilter({
  label,
  options,
  value,
  onChange,
  allowCustom = false,
  customPlaceholder,
  maxItems,
  onOpenChange,
  emptyText,
}: MultiSelectFilterProps) {
  const { t } = useTranslation('proxies');
  const baseId = useId();
  // Selected values that are not in the option list stay visible and removable.
  const merged = [
    ...options,
    ...value.filter((v) => !options.some((o) => o.value === v)).map((v) => ({ value: v, label: v })),
  ];
  const selectedLabel =
    value.length === 1 ? (merged.find((o) => o.value === value[0])?.label ?? value[0]) : String(value.length);

  const toggle = (optionValue: string, checked: boolean) => {
    if (checked) {
      if (value.includes(optionValue) || (maxItems !== undefined && value.length >= maxItems)) return;
      onChange([...value, optionValue]);
    } else {
      onChange(value.filter((v) => v !== optionValue));
    }
  };

  return (
    <Popover onOpenChange={onOpenChange}>
      <PopoverTrigger asChild>
        <Button variant="outline" size="sm" className={cn('h-8', value.length === 0 && 'border-dashed')}>
          <ListFilterIcon />
          {label}
          {value.length > 0 && (
            <Badge variant="secondary" className="max-w-32 truncate">
              {selectedLabel}
            </Badge>
          )}
        </Button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-64 p-2">
        <div role="group" aria-label={label} className="grid max-h-72 gap-0.5 overflow-y-auto">
          {merged.length === 0 && (
            <p className="px-2 py-1.5 text-xs text-muted-foreground">{emptyText ?? t('filters.noOptions')}</p>
          )}
          {merged.map((option, index) => {
            const id = `${baseId}-${index}`;
            return (
              <div key={option.value} className="flex items-center gap-2 rounded px-2 py-1.5 hover:bg-accent">
                <Checkbox
                  id={id}
                  checked={value.includes(option.value)}
                  onCheckedChange={(checked) => toggle(option.value, checked === true)}
                />
                <Label htmlFor={id} className="min-w-0 flex-1 cursor-pointer truncate font-normal">
                  {option.label}
                </Label>
              </div>
            );
          })}
        </div>
        {allowCustom && (
          <div className="mt-2 border-t pt-2">
            <FormField label={t('filters.customValue')}>
              <TagsInput
                value={value}
                onChange={onChange}
                maxTags={maxItems}
                placeholder={customPlaceholder}
              />
            </FormField>
          </div>
        )}
        {value.length > 0 && (
          <Button variant="ghost" size="sm" className="mt-1 w-full" onClick={() => onChange([])}>
            {t('common:actions.clear')}
          </Button>
        )}
      </PopoverContent>
    </Popover>
  );
}
