import { create } from '@bufbuild/protobuf';
import { timestampFromMs } from '@bufbuild/protobuf/wkt';
import { format } from 'date-fns';

import { TimeRangeSchema, type TimeRange } from '@/gen/spinneret/v1/common_pb';
import { DAY_MS, HOUR_MS, MINUTE_MS } from '@/lib/time';

import { readEnum, readString, type SearchRecord, type SearchValue } from './searchParams';

/** Relative time range presets offered by the explorers. */
export const TIME_RANGE_PRESETS = ['15m', '1h', '6h', '24h', '7d'] as const;
export type TimeRangePreset = (typeof TIME_RANGE_PRESETS)[number];
export const CUSTOM_RANGE = 'custom';

const PRESET_MS: Record<TimeRangePreset, number> = {
  '15m': 15 * MINUTE_MS,
  '1h': HOUR_MS,
  '6h': 6 * HOUR_MS,
  '24h': DAY_MS,
  '7d': 7 * DAY_MS,
};

/** A selected time range: a preset relative to "now" or absolute bounds in epoch ms. */
export type TimeRangeSelection =
  { kind: 'preset'; preset: TimeRangePreset } | { kind: 'custom'; from: number; to: number };

export function presetSelection(preset: TimeRangePreset): TimeRangeSelection {
  return { kind: 'preset', preset };
}

export function presetDurationMs(preset: TimeRangePreset): number {
  return PRESET_MS[preset];
}

/** True when a custom range is complete and ordered. */
export function isValidCustomRange(from: number, to: number): boolean {
  return Number.isFinite(from) && Number.isFinite(to) && to > from;
}

/**
 * Converts a selection to the protobuf TimeRange of a request. Presets end at
 * `anchorMs` (the time the first page was requested) so that every page of a
 * query covers the same window.
 */
export function toTimeRange(selection: TimeRangeSelection, anchorMs: number): TimeRange {
  if (selection.kind === 'custom') {
    return create(TimeRangeSchema, {
      start: timestampFromMs(selection.from),
      end: timestampFromMs(selection.to),
    });
  }
  return create(TimeRangeSchema, {
    start: timestampFromMs(anchorMs - PRESET_MS[selection.preset]),
    end: timestampFromMs(anchorMs),
  });
}

function parseInstant(text: string): number | undefined {
  if (text === '') return undefined;
  const ms = /^\d+$/.test(text) ? Number(text) : Date.parse(text);
  return Number.isFinite(ms) ? ms : undefined;
}

/**
 * Reads `range` (preset or "custom") with `from`/`to` (ISO 8601 or epoch ms).
 * Invalid custom bounds fall back to `fallback`.
 */
export function readTimeRange(search: SearchRecord, fallback: TimeRangePreset): TimeRangeSelection {
  const range = readEnum(search, 'range', [...TIME_RANGE_PRESETS, CUSTOM_RANGE], fallback);
  if (range !== CUSTOM_RANGE) return presetSelection(range);
  const from = parseInstant(readString(search, 'from', 40));
  const to = parseInstant(readString(search, 'to', 40));
  if (from === undefined || to === undefined || !isValidCustomRange(from, to)) {
    return presetSelection(fallback);
  }
  return { kind: 'custom', from, to };
}

/** Search params of a selection; the default preset is omitted. */
export function timeRangeToSearch(
  selection: TimeRangeSelection,
  fallback: TimeRangePreset,
): Record<string, SearchValue> {
  if (selection.kind === 'custom') {
    return {
      range: CUSTOM_RANGE,
      from: new Date(selection.from).toISOString(),
      to: new Date(selection.to).toISOString(),
    };
  }
  return { range: selection.preset === fallback ? undefined : selection.preset };
}

/** Value for <input type="datetime-local"> in local time. */
export function toDateTimeLocal(ms: number): string {
  return format(new Date(ms), "yyyy-MM-dd'T'HH:mm");
}

/** Parses an <input type="datetime-local"> value (local time); undefined when empty or invalid. */
export function fromDateTimeLocal(value: string): number | undefined {
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2})?$/.test(value)) return undefined;
  const ms = new Date(value).getTime();
  return Number.isFinite(ms) ? ms : undefined;
}
