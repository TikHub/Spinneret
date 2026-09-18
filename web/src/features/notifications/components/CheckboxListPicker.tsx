import { ChevronsUpDownIcon } from 'lucide-react';
import { useId, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';

import { Button } from '@/components/ui/button';
import { Checkbox } from '@/components/ui/checkbox';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';
import { cn } from '@/lib/utils';

export interface PickerOption {
  value: string;
  label: ReactNode;
  description?: ReactNode;
}

export interface CheckboxListPickerProps {
  options: readonly PickerOption[];
  value: readonly string[];
  onChange: (value: string[]) => void;
  /** Trigger text. */
  summary: ReactNode;
  disabled?: boolean;
  id?: string;
  className?: string;
  'aria-invalid'?: boolean;
  'aria-describedby'?: string;
}

/** Multi-select as a checkbox list in a popover, with select all / clear. */
export function CheckboxListPicker({
  options,
  value,
  onChange,
  summary,
  disabled,
  id,
  className,
  ...aria
}: CheckboxListPickerProps) {
  const { t } = useTranslation('notifications');
  const baseId = useId();
  const selected = new Set(value);
  const toggle = (option: string, checked: boolean) => {
    const next = checked ? [...value, option] : value.filter((v) => v !== option);
    // Keep the option order stable.
    onChange(options.map((o) => o.value).filter((v) => next.includes(v)));
  };

  return (
    <Popover>
      <PopoverTrigger asChild>
        <Button
          id={id}
          type="button"
          variant="outline"
          disabled={disabled}
          className={cn(
            'w-full justify-between font-normal',
            aria['aria-invalid'] && 'border-destructive',
            className,
          )}
          {...aria}
        >
          <span className="truncate">{summary}</span>
          <ChevronsUpDownIcon className="opacity-50" />
        </Button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-(--radix-popover-trigger-width) min-w-80 p-0">
        <div className="flex items-center justify-between border-b px-3 py-1.5">
          <span className="text-xs text-muted-foreground">
            {t('picker.selected', { count: value.length, total: options.length })}
          </span>
          <span className="flex gap-1">
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="h-7"
              onClick={() => onChange(options.map((o) => o.value))}
            >
              {t('picker.all')}
            </Button>
            <Button type="button" variant="ghost" size="sm" className="h-7" onClick={() => onChange([])}>
              {t('common:actions.clear')}
            </Button>
          </span>
        </div>
        <ul className="max-h-80 overflow-y-auto p-1">
          {options.map((option) => {
            const optionId = `${baseId}-${option.value}`;
            return (
              <li key={option.value}>
                <label
                  htmlFor={optionId}
                  className="flex cursor-pointer items-start gap-2 rounded-sm px-2 py-1.5 hover:bg-accent"
                >
                  <Checkbox
                    id={optionId}
                    className="mt-0.5"
                    checked={selected.has(option.value)}
                    onCheckedChange={(checked) => toggle(option.value, checked === true)}
                  />
                  <span className="grid gap-0.5">
                    <span className="text-sm">{option.label}</span>
                    {option.description && (
                      <span className="text-xs text-muted-foreground">{option.description}</span>
                    )}
                  </span>
                </label>
              </li>
            );
          })}
          {options.length === 0 && (
            <li className="px-2 py-3 text-center text-xs text-muted-foreground">{t('picker.noOptions')}</li>
          )}
        </ul>
      </PopoverContent>
    </Popover>
  );
}
