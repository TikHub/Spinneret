import { useEffect, useState, type InputHTMLAttributes } from 'react';

import { Input } from '@/components/ui/input';
import { SEARCH_DEBOUNCE_MS, useDebouncedValue } from '@/lib/useDebouncedValue';

export interface DebouncedInputProps extends Omit<
  InputHTMLAttributes<HTMLInputElement>,
  'value' | 'onChange'
> {
  value: string;
  /** Called with the debounced, trimmed value. */
  onChange: (value: string) => void;
  debounceMs?: number;
}

/** Text input that reports changes after typing pauses and adopts external resets. */
export function DebouncedInput({
  value,
  onChange,
  debounceMs = SEARCH_DEBOUNCE_MS,
  ...props
}: DebouncedInputProps) {
  const [draft, setDraft] = useState(value);
  const [lastValue, setLastValue] = useState(value);
  if (value !== lastValue) {
    setLastValue(value);
    setDraft(value);
  }
  const debounced = useDebouncedValue(draft, debounceMs);
  useEffect(() => {
    const next = debounced.trim();
    if (next !== value) onChange(next);
    // Only the debounced draft triggers a change; external values are adopted above.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [debounced]);
  return <Input value={draft} onChange={(e) => setDraft(e.target.value)} {...props} />;
}
