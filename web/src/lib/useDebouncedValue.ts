import { useEffect, useState } from 'react';

/** Default debounce for search inputs. */
export const SEARCH_DEBOUNCE_MS = 300;

/** Returns `value` after it has not changed for `delayMs`. */
export function useDebouncedValue<T>(value: T, delayMs: number = SEARCH_DEBOUNCE_MS): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const timer = setTimeout(() => setDebounced(value), delayMs);
    return () => clearTimeout(timer);
  }, [value, delayMs]);
  return debounced;
}
