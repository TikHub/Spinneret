/**
 * Helpers to read and write loosely typed route search params. TanStack
 * Router JSON-parses values, so "200" may arrive as the number 200 and lists
 * may arrive as arrays or comma-separated strings; readers accept all forms.
 */

/** Search params as delivered by the router (see PageSearch in src/router.tsx). */
export type SearchValue = string | number | boolean | string[] | undefined;
export type SearchRecord = Record<string, SearchValue>;

/** Max length accepted for free-text filters (IDs, node names). */
export const MAX_TEXT_FILTER_LENGTH = 128;

/** Reads a trimmed string; numbers are converted, other values ignored. */
export function readString(search: SearchRecord, key: string, maxLength = MAX_TEXT_FILTER_LENGTH): string {
  const value = search[key];
  const text = typeof value === 'string' ? value : typeof value === 'number' ? String(value) : '';
  return text.trim().slice(0, maxLength);
}

/** Reads a string restricted to allowed values, falling back to `fallback`. */
export function readEnum<T extends string>(
  search: SearchRecord,
  key: string,
  allowed: readonly T[],
  fallback: T,
): T {
  const value = readString(search, key);
  return (allowed as readonly string[]).includes(value) ? (value as T) : fallback;
}

/** Reads a list from an array or a comma-separated string (deduplicated, empty items dropped). */
export function readList(search: SearchRecord, key: string): string[] {
  const value = search[key];
  const items = Array.isArray(value)
    ? value
    : typeof value === 'string'
      ? value.split(',')
      : typeof value === 'number'
        ? [String(value)]
        : [];
  return [...new Set(items.map((item) => String(item).trim()).filter((item) => item !== ''))];
}

/** Reads an integer within [min, max]; undefined when absent or invalid. */
export function readInt(search: SearchRecord, key: string, min: number, max: number): number | undefined {
  const value = search[key];
  const n = typeof value === 'number' ? value : typeof value === 'string' ? Number(value.trim()) : NaN;
  if (typeof value === 'string' && value.trim() === '') return undefined;
  if (!Number.isInteger(n) || n < min || n > max) return undefined;
  return n;
}

/** Drops empty values so that default filters produce clean URLs. */
export function compactSearch(values: Record<string, SearchValue>): SearchRecord {
  const out: SearchRecord = {};
  for (const [key, value] of Object.entries(values)) {
    if (value === undefined || value === '' || value === false) continue;
    if (Array.isArray(value)) {
      if (value.length > 0) out[key] = value.join(',');
      continue;
    }
    out[key] = value;
  }
  return out;
}

/** Parses a user-typed integer (e.g. an HTTP status); undefined when empty or invalid. */
export function parseIntInput(input: string, min: number, max: number): number | undefined {
  const text = input.trim();
  if (!/^\d+$/.test(text)) return undefined;
  const n = Number(text);
  return n >= min && n <= max ? n : undefined;
}
