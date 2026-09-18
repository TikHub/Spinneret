import { timestampFromMs } from '@bufbuild/protobuf/wkt';
import { describe, expect, it } from 'vitest';

import { clampRatio, formatCountdown, openPeriod } from './countdown';

describe('formatCountdown', () => {
  it('formats seconds, minutes, hours and days', () => {
    expect(formatCountdown(0)).toBe('0s');
    expect(formatCountdown(-5000)).toBe('0s');
    expect(formatCountdown(Number.NaN)).toBe('0s');
    expect(formatCountdown(999)).toBe('1s');
    expect(formatCountdown(45_000)).toBe('45s');
    expect(formatCountdown(60_000)).toBe('1m 00s');
    expect(formatCountdown(245_000)).toBe('4m 05s');
    expect(formatCountdown(3_600_000)).toBe('1h 00m 00s');
    expect(formatCountdown(2 * 3_600_000 + 3 * 60_000 + 9_000)).toBe('2h 03m 09s');
    expect(formatCountdown(3 * 86_400_000 + 4 * 3_600_000 + 10 * 60_000 + 59_000)).toBe('3d 04h 10m');
  });

  it('rounds partial seconds up so the countdown never shows 0s early', () => {
    expect(formatCountdown(59_001)).toBe('1m 00s');
    expect(formatCountdown(1)).toBe('1s');
  });
});

describe('openPeriod', () => {
  const now = 1_700_000_000_000;

  it('is none unless the breaker is open', () => {
    expect(openPeriod('closed', timestampFromMs(now + 1000), now)).toEqual({ kind: 'none' });
    expect(openPeriod('half_open', undefined, now)).toEqual({ kind: 'none' });
  });

  it('is indefinite when an open breaker has no end', () => {
    expect(openPeriod('open', undefined, now)).toEqual({ kind: 'indefinite' });
  });

  it('computes the remaining time (0 once elapsed)', () => {
    const period = openPeriod('open', timestampFromMs(now + 90_000), now);
    expect(period).toMatchObject({ kind: 'until', remainingMs: 90_000 });
    expect(period.kind === 'until' && formatCountdown(period.remainingMs)).toBe('1m 30s');
    expect(openPeriod('open', timestampFromMs(now - 5000), now)).toMatchObject({
      kind: 'until',
      remainingMs: 0,
    });
  });
});

describe('clampRatio', () => {
  it('keeps ratios within 0..1', () => {
    expect(clampRatio(0.42)).toBe(0.42);
    expect(clampRatio(-1)).toBe(0);
    expect(clampRatio(3)).toBe(1);
    expect(clampRatio(Number.NaN)).toBe(0);
  });
});
