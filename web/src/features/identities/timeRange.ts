import { format } from 'date-fns';

/** Value for <input type="datetime-local"> in local time ("2026-01-02T15:04"). */
export function toDateTimeLocalValue(ms: number): string {
  if (!Number.isFinite(ms)) return '';
  return format(new Date(ms), "yyyy-MM-dd'T'HH:mm");
}

/** Parses a datetime-local value (local time) into epoch ms; undefined when empty or invalid. */
export function fromDateTimeLocalValue(value: string): number | undefined {
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::(\d{2}))?$/.exec(value.trim());
  if (!match) return undefined;
  const [, y, mo, d, h, mi, s] = match;
  const date = new Date(Number(y), Number(mo) - 1, Number(d), Number(h), Number(mi), Number(s ?? '0'));
  if (
    date.getFullYear() !== Number(y) ||
    date.getMonth() !== Number(mo) - 1 ||
    date.getDate() !== Number(d)
  ) {
    return undefined;
  }
  return date.getTime();
}

export type TimeRangeError = 'startRequired' | 'startInvalid' | 'endInvalid' | 'order';

/** Validates a start (required) / end (optional) pair of datetime-local values. */
export function validateTimeRange(start: string, end: string): TimeRangeError | undefined {
  if (start.trim() === '') return 'startRequired';
  const startMs = fromDateTimeLocalValue(start);
  if (startMs === undefined) return 'startInvalid';
  if (end.trim() === '') return undefined;
  const endMs = fromDateTimeLocalValue(end);
  if (endMs === undefined) return 'endInvalid';
  return startMs < endMs ? undefined : 'order';
}
