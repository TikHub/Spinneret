import { useEffect, useState } from 'react';

import { Input } from '@/components/ui/input';
import { SEARCH_DEBOUNCE_MS, useDebouncedValue } from '@/lib/useDebouncedValue';
import { cn } from '@/lib/utils';

import { parseIntInput } from './searchParams';

export interface IntFilterInputProps {
  /** Committed value; undefined = no filter. */
  value: number | undefined;
  onChange: (value: number | undefined) => void;
  min: number;
  max: number;
  /** Placeholder, also used as the accessible label. */
  label: string;
  className?: string;
}

function toText(value: number | undefined): string {
  return value === undefined ? '' : String(value);
}

/** Debounced integer filter; invalid input is flagged and not committed. */
export function IntFilterInput({ value, onChange, min, max, label, className }: IntFilterInputProps) {
  const [draft, setDraft] = useState(toText(value));
  const [lastValue, setLastValue] = useState(value);
  // Adopt external changes (reset, back navigation) without an effect.
  if (value !== lastValue) {
    setLastValue(value);
    setDraft(toText(value));
  }
  const debounced = useDebouncedValue(draft, SEARCH_DEBOUNCE_MS);
  const parsed = parseIntInput(debounced, min, max);
  const invalid = draft.trim() !== '' && parseIntInput(draft, min, max) === undefined;

  useEffect(() => {
    const text = debounced.trim();
    if (text === '' && value !== undefined) onChange(undefined);
    else if (parsed !== undefined && parsed !== value) onChange(parsed);
    // Only react to the debounced draft; `value` changes are adopted above.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [debounced]);

  return (
    <Input
      inputMode="numeric"
      value={draft}
      onChange={(e) => setDraft(e.target.value)}
      placeholder={label}
      aria-label={label}
      aria-invalid={invalid || undefined}
      className={cn('h-8 w-32', className)}
    />
  );
}
