import { type RequestEvent } from '@/gen/spinneret/v1/dashboard_pb';

/**
 * Unique row IDs of a page of request events. Events are identified by lease
 * and report ID; ClickHouse (a plain MergeTree) can return duplicated rows
 * after retried inserts, so repeats get an occurrence suffix to keep React
 * keys unique.
 */
export function requestRowIds(events: readonly RequestEvent[]): ReadonlyMap<RequestEvent, string> {
  const occurrences = new Map<string, number>();
  const ids = new Map<RequestEvent, string>();
  events.forEach((event, index) => {
    const base = event.reportId ? `${event.leaseId}:${event.reportId}` : `row-${index}`;
    const seen = occurrences.get(base) ?? 0;
    occurrences.set(base, seen + 1);
    ids.set(event, seen === 0 ? base : `${base}#${seen}`);
  });
  return ids;
}
