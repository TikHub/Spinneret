import { ChevronDownIcon } from 'lucide-react';
import { type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';

import { Button } from '@/components/ui/button';
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import { cn } from '@/lib/utils';

export interface MultiSelectOption {
  value: string;
  label: ReactNode;
}

export interface MultiSelectMenuProps {
  /** Filter name (trigger label and menu heading). */
  label: string;
  options: readonly MultiSelectOption[];
  value: readonly string[];
  onChange: (value: string[]) => void;
  /** Trigger text when nothing is selected. */
  emptyText: string;
  className?: string;
}

/** Dropdown with checkboxes for multi-value filters; keeps option order in the result. */
export function MultiSelectMenu({
  label,
  options,
  value,
  onChange,
  emptyText,
  className,
}: MultiSelectMenuProps) {
  const { t } = useTranslation();
  const toggle = (option: string, checked: boolean) => {
    const next = new Set(value);
    if (checked) next.add(option);
    else next.delete(option);
    onChange(options.map((o) => o.value).filter((v) => next.has(v)));
  };

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="outline"
          size="sm"
          className={cn('justify-between font-normal', className)}
          aria-label={`${label}: ${value.length === 0 ? emptyText : value.length}`}
        >
          <span className="truncate">
            {label}
            <span className="text-muted-foreground">
              {': '}
              {value.length === 0 ? emptyText : value.length}
            </span>
          </span>
          <ChevronDownIcon className="opacity-50" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="max-h-96 min-w-48">
        <DropdownMenuLabel>{label}</DropdownMenuLabel>
        <DropdownMenuSeparator />
        {options.map((option) => (
          <DropdownMenuCheckboxItem
            key={option.value}
            checked={value.includes(option.value)}
            onCheckedChange={(checked) => toggle(option.value, Boolean(checked))}
            onSelect={(event) => event.preventDefault()}
          >
            {option.label}
          </DropdownMenuCheckboxItem>
        ))}
        {value.length > 0 && (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuItem onSelect={() => onChange([])}>{t('actions.clear')}</DropdownMenuItem>
          </>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
