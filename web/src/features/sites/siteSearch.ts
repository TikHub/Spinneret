/**
 * Reads a name from a URL search value; undefined when absent or empty. The
 * router parses hand-typed values as JSON, so "?site=123" (a valid site name)
 * arrives as the number 123.
 */
export function searchName(value: unknown): string | undefined {
  if (typeof value === 'string') return value === '' ? undefined : value;
  if (typeof value === 'number' && Number.isSafeInteger(value) && value >= 0) return String(value);
  return undefined;
}
