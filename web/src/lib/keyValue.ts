export interface KeyValuePair {
  key: string;
  value: string;
}

/** Converts pairs to a record (later duplicates win, empty keys are dropped). */
export function pairsToRecord(pairs: readonly KeyValuePair[]): Record<string, string> {
  const out: Record<string, string> = {};
  for (const { key, value } of pairs) {
    if (key.trim()) out[key.trim()] = value;
  }
  return out;
}

/** Converts a record to pairs sorted by key. */
export function recordToPairs(record: Readonly<Record<string, string>>): KeyValuePair[] {
  return Object.entries(record)
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([key, value]) => ({ key, value }));
}

/** Keys that appear more than once (after trimming). */
export function duplicateKeys(pairs: readonly KeyValuePair[]): Set<string> {
  const seen = new Set<string>();
  const dupes = new Set<string>();
  for (const { key } of pairs) {
    const k = key.trim();
    if (!k) continue;
    if (seen.has(k)) dupes.add(k);
    seen.add(k);
  }
  return dupes;
}
