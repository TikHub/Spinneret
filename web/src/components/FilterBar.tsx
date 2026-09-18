import { SearchIcon, XIcon } from 'lucide-react';
import { createContext, useContext, useEffect, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';

import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { SEARCH_DEBOUNCE_MS, useDebouncedValue } from '@/lib/useDebouncedValue';
import { cn } from '@/lib/utils';

/**
 * Incremented by FilterBar on every "reset filters" click. Search inputs inside
 * the bar drop their pending (not yet debounced) draft when it changes, which
 * they cannot detect from `value` alone when the draft was never applied.
 */
const FilterResetContext = createContext(0);

export interface SearchInputProps {
  value: string;
  /** Called with the debounced value. */
  onChange: (value: string) => void;
  placeholder?: string;
  debounceMs?: number;
  className?: string;
}

/** Search box with debounce and a clear button. */
export function SearchInput({
  value,
  onChange,
  placeholder,
  debounceMs = SEARCH_DEBOUNCE_MS,
  className,
}: SearchInputProps) {
  const { t } = useTranslation();
  const resetGeneration = useContext(FilterResetContext);
  const [draft, setDraft] = useState(value);
  const [adopted, setAdopted] = useState({ value, resetGeneration });
  // Adopt external value changes and filter resets without an effect. A reset
  // also discards a draft still waiting for the debounce: otherwise the timer
  // would re-apply it right after the reset when the applied value was unchanged.
  if (value !== adopted.value || resetGeneration !== adopted.resetGeneration) {
    setAdopted({ value, resetGeneration });
    setDraft(value);
  }
  const debounced = useDebouncedValue(draft, debounceMs);
  useEffect(() => {
    if (debounced !== value) onChange(debounced);
    // Only react to the debounced draft; `value` changes are adopted above.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [debounced]);

  return (
    <div className={cn('relative w-full sm:w-64', className)}>
      <SearchIcon className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
      <Input
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        placeholder={placeholder ?? t('filters.search')}
        className="h-8 pr-8 pl-8"
        aria-label={placeholder ?? t('actions.search')}
      />
      {draft && (
        <button
          type="button"
          className="absolute top-1/2 right-2 -translate-y-1/2 rounded text-muted-foreground hover:text-foreground"
          aria-label={t('actions.clear')}
          onClick={() => {
            setDraft('');
            onChange('');
          }}
        >
          <XIcon className="size-4" />
        </button>
      )}
    </div>
  );
}

export interface FilterBarProps {
  /** Search box configuration; omitted when the page has no free-text search. */
  search?: SearchInputProps;
  /** Filter controls (selects, toggles). */
  children?: ReactNode;
  /** Number of active filters; shows the reset button when > 0. */
  activeCount?: number;
  onReset?: () => void;
  /** Right-aligned content (e.g. refresh button). */
  actions?: ReactNode;
  className?: string;
}

/** Horizontal filter toolbar that wraps on narrow screens. */
export function FilterBar({
  search,
  children,
  activeCount = 0,
  onReset,
  actions,
  className,
}: FilterBarProps) {
  const { t } = useTranslation();
  const [resetGeneration, setResetGeneration] = useState(0);
  const reset = () => {
    setResetGeneration((n) => n + 1);
    onReset?.();
  };
  return (
    <FilterResetContext.Provider value={resetGeneration}>
      <div className={cn('flex flex-wrap items-center gap-2', className)} role="search">
        {search && <SearchInput {...search} />}
        {children}
        {onReset && activeCount > 0 && (
          <Button variant="ghost" size="sm" onClick={reset}>
            <XIcon />
            {t('filters.reset')}
            <span className="text-muted-foreground">({activeCount})</span>
          </Button>
        )}
        {actions && <div className="ml-auto flex items-center gap-2">{actions}</div>}
      </div>
    </FilterResetContext.Provider>
  );
}
