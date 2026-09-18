import { timestampMs } from '@bufbuild/protobuf/wkt';
import { describe, expect, it } from 'vitest';

import { DAY_MS, HOUR_MS, MINUTE_MS } from '@/lib/time';

import {
  fromDateTimeLocal,
  presetDurationMs,
  presetSelection,
  readTimeRange,
  TIME_RANGE_PRESETS,
  timeRangeToSearch,
  toDateTimeLocal,
  toTimeRange,
} from './timeRange';

const ANCHOR = Date.UTC(2026, 8, 17, 10, 0, 0);

function bounds(range: ReturnType<typeof toTimeRange>) {
  if (!range.start || !range.end) throw new Error('unset bounds');
  return [timestampMs(range.start), timestampMs(range.end)];
}

describe('toTimeRange', () => {
  it('converts every preset to a window ending at the anchor', () => {
    const expected: Record<string, number> = {
      '15m': 15 * MINUTE_MS,
      '1h': HOUR_MS,
      '6h': 6 * HOUR_MS,
      '24h': DAY_MS,
      '7d': 7 * DAY_MS,
    };
    for (const preset of TIME_RANGE_PRESETS) {
      expect(presetDurationMs(preset)).toBe(expected[preset]);
      expect(bounds(toTimeRange(presetSelection(preset), ANCHOR))).toEqual([
        ANCHOR - (expected[preset] ?? 0),
        ANCHOR,
      ]);
    }
  });

  it('keeps custom bounds regardless of the anchor', () => {
    const range = toTimeRange({ kind: 'custom', from: ANCHOR - 5 * MINUTE_MS, to: ANCHOR }, 0);
    expect(bounds(range)).toEqual([ANCHOR - 5 * MINUTE_MS, ANCHOR]);
  });
});

describe('readTimeRange / timeRangeToSearch', () => {
  it('reads presets and falls back for unknown values', () => {
    expect(readTimeRange({ range: '6h' }, '1h')).toEqual(presetSelection('6h'));
    expect(readTimeRange({ range: '2y' }, '1h')).toEqual(presetSelection('1h'));
    expect(readTimeRange({}, '24h')).toEqual(presetSelection('24h'));
  });

  it('round-trips custom ranges as ISO strings and accepts epoch ms', () => {
    const selection = { kind: 'custom' as const, from: ANCHOR - HOUR_MS, to: ANCHOR };
    const search = timeRangeToSearch(selection, '1h');
    expect(search).toEqual({
      range: 'custom',
      from: '2026-09-17T09:00:00.000Z',
      to: '2026-09-17T10:00:00.000Z',
    });
    expect(readTimeRange(search as Record<string, string>, '1h')).toEqual(selection);
    expect(readTimeRange({ range: 'custom', from: ANCHOR - HOUR_MS, to: ANCHOR }, '1h')).toEqual(selection);
  });

  it('rejects incomplete or reversed custom ranges', () => {
    expect(readTimeRange({ range: 'custom', from: '2026-09-17T10:00:00Z' }, '1h')).toEqual(
      presetSelection('1h'),
    );
    expect(
      readTimeRange({ range: 'custom', from: '2026-09-17T10:00:00Z', to: '2026-09-17T09:00:00Z' }, '1h'),
    ).toEqual(presetSelection('1h'));
  });

  it('omits the default preset', () => {
    expect(timeRangeToSearch(presetSelection('1h'), '1h')).toEqual({ range: undefined });
    expect(timeRangeToSearch(presetSelection('7d'), '1h')).toEqual({ range: '7d' });
  });
});

describe('datetime-local conversion', () => {
  it('round-trips local minutes', () => {
    const ms = new Date(2026, 8, 17, 8, 30).getTime();
    expect(toDateTimeLocal(ms)).toBe('2026-09-17T08:30');
    expect(fromDateTimeLocal('2026-09-17T08:30')).toBe(ms);
    expect(fromDateTimeLocal('')).toBeUndefined();
    expect(fromDateTimeLocal('yesterday')).toBeUndefined();
  });
});
