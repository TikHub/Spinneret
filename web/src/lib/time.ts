import { create } from '@bufbuild/protobuf';
import { timestampDate, timestampFromMs, type Timestamp } from '@bufbuild/protobuf/wkt';
import { format, formatDistanceStrict, type Locale } from 'date-fns';
import { enUS } from 'date-fns/locale/en-US';
import { zhCN } from 'date-fns/locale/zh-CN';

import { TimeRangeSchema, type TimeRange } from '@/gen/spinneret/v1/common_pb';

/** Values accepted by the time helpers. Numbers are epoch milliseconds. */
export type TimeInput = Timestamp | Date | number | string | null | undefined;

export const SECOND_MS = 1000;
export const MINUTE_MS = 60 * SECOND_MS;
export const HOUR_MS = 60 * MINUTE_MS;
export const DAY_MS = 24 * HOUR_MS;

function isTimestamp(value: unknown): value is Timestamp {
  return typeof value === 'object' && value !== null && 'seconds' in value && 'nanos' in value;
}

/** Converts a protobuf Timestamp, Date, epoch ms or ISO string to a Date (undefined when unset or invalid). */
export function toDate(value: TimeInput): Date | undefined {
  if (value === null || value === undefined || value === '') return undefined;
  let date: Date;
  if (value instanceof Date) {
    date = value;
  } else if (isTimestamp(value)) {
    if (value.seconds === 0n && value.nanos === 0) return undefined;
    date = timestampDate(value);
  } else {
    date = new Date(value);
  }
  return Number.isNaN(date.getTime()) ? undefined : date;
}

/** Converts a Date or epoch ms to a protobuf Timestamp. */
export function toTimestamp(value: Date | number): Timestamp {
  return timestampFromMs(value instanceof Date ? value.getTime() : value);
}

/** date-fns locale for a UI language. */
export function dateLocale(language: string | undefined): Locale {
  return language?.toLowerCase().startsWith('zh') ? zhCN : enUS;
}

/** Absolute local time "yyyy-MM-dd HH:mm:ss"; empty string when unset. */
export function formatDateTime(value: TimeInput, withSeconds = true): string {
  const date = toDate(value);
  if (!date) return '';
  return format(date, withSeconds ? 'yyyy-MM-dd HH:mm:ss' : 'yyyy-MM-dd HH:mm');
}

/** Local clock time "HH:mm:ss" (or "HH:mm"); empty string when unset. */
export function formatClock(value: TimeInput, withSeconds = true): string {
  const date = toDate(value);
  if (!date) return '';
  return format(date, withSeconds ? 'HH:mm:ss' : 'HH:mm');
}

/** Relative time such as "3 minutes ago" or "in 5 minutes"; empty string when unset. */
export function formatRelative(value: TimeInput, now: number = Date.now(), language?: string): string {
  const date = toDate(value);
  if (!date) return '';
  return formatDistanceStrict(date, now, { addSuffix: true, locale: dateLocale(language) });
}

/** Milliseconds from now until the value (0 when in the past or unset). */
export function remainingMs(value: TimeInput, now: number = Date.now()): number {
  const date = toDate(value);
  if (!date) return 0;
  return Math.max(0, date.getTime() - now);
}

/** Half-open TimeRange covering the last `durationMs` milliseconds. */
export function lastTimeRange(durationMs: number, now: number = Date.now()): TimeRange {
  return create(TimeRangeSchema, {
    start: timestampFromMs(now - durationMs),
    end: timestampFromMs(now),
  });
}
