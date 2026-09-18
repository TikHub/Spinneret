import { create } from '@bufbuild/protobuf';
import { timestampFromMs } from '@bufbuild/protobuf/wkt';
import { describe, expect, it } from 'vitest';

import { EndpointHotStateSchema, IdentityHotStateSchema } from '@/gen/spinneret/v1/identity_admin_pb';

import {
  clampScore,
  countdownParts,
  endpointAvailability,
  formatCountdown,
  formatScore,
  scoreTone,
  siteBlockers,
  sortEndpointGroups,
} from './hotState';

const NOW = 1_700_000_000_000;

describe('scores', () => {
  it('clamps and classifies', () => {
    expect(clampScore(-5)).toBe(0);
    expect(clampScore(150)).toBe(100);
    expect(clampScore(Number.NaN)).toBe(0);
    expect(scoreTone(85)).toBe('good');
    expect(scoreTone(60)).toBe('good');
    expect(scoreTone(45)).toBe('warn');
    expect(scoreTone(29.9)).toBe('bad');
    expect(formatScore(72.456)).toBe('72.5');
    expect(formatScore(80)).toBe('80');
  });
});

describe('formatCountdown', () => {
  it('formats future times and hides past ones', () => {
    expect(formatCountdown(timestampFromMs(NOW + 4 * 60_000 + 12_000), NOW)).toBe('4m 12s');
    expect(formatCountdown(timestampFromMs(NOW + 26 * 3_600_000), NOW)).toBe('1d 2h');
    expect(formatCountdown(timestampFromMs(NOW + 400), NOW)).toBe('1s');
    expect(formatCountdown(timestampFromMs(NOW - 1000), NOW)).toBe('');
    expect(formatCountdown(undefined, NOW)).toBe('');
  });

  it('exposes the parts for localized units', () => {
    const until = timestampFromMs(NOW + 3 * 3_600_000 + 5 * 60_000 + 9_000);
    expect(countdownParts(until, NOW)).toEqual([
      { unit: 'h', value: 3 },
      { unit: 'm', value: 5 },
    ]);
    expect(countdownParts(until, NOW, 3)).toHaveLength(3);
    expect(formatCountdown(until, NOW, (p) => `${p.value}${p.unit === 'h' ? '小时' : '分'}`)).toBe(
      '3小时 5分',
    );
    expect(countdownParts(timestampFromMs(NOW - 1), NOW)).toEqual([]);
  });
});

describe('endpointAvailability', () => {
  it('prefers cooldown, then reuse, then available_at', () => {
    const base = { endpointGroup: 'search', client: 'web' };
    expect(
      endpointAvailability(
        create(EndpointHotStateSchema, {
          ...base,
          cooldownUntil: timestampFromMs(NOW + 1000),
          reuseUntil: timestampFromMs(NOW + 5000),
        }),
        NOW,
      ),
    ).toBe('cooldown');
    expect(
      endpointAvailability(
        create(EndpointHotStateSchema, { ...base, reuseUntil: timestampFromMs(NOW + 5) }),
        NOW,
      ),
    ).toBe('reuse');
    expect(
      endpointAvailability(
        create(EndpointHotStateSchema, { ...base, availableAt: timestampFromMs(NOW + 5) }),
        NOW,
      ),
    ).toBe('waiting');
    expect(
      endpointAvailability(
        create(EndpointHotStateSchema, {
          ...base,
          cooldownUntil: timestampFromMs(NOW - 5),
          inReadyQueue: true,
        }),
        NOW,
      ),
    ).toBe('ready');
    expect(endpointAvailability(create(EndpointHotStateSchema, base), NOW)).toBe('queued');
  });

  it('sorts groups by client and name', () => {
    const groups = [
      create(EndpointHotStateSchema, { client: 'web', endpointGroup: 'search' }),
      create(EndpointHotStateSchema, { client: 'app', endpointGroup: 'feed' }),
      create(EndpointHotStateSchema, { client: 'web', endpointGroup: '_default' }),
    ];
    expect(sortEndpointGroups(groups).map((g) => `${g.client}/${g.endpointGroup}`)).toEqual([
      'app/feed',
      'web/_default',
      'web/search',
    ]);
  });
});

describe('siteBlockers', () => {
  it('lists active site-level intervals soonest first', () => {
    const hot = create(IdentityHotStateSchema, {
      siteCooldownUntil: timestampFromMs(NOW + 60_000),
      siteReuseUntil: timestampFromMs(NOW - 1),
      exclusiveUntil: timestampFromMs(NOW + 10_000),
    });
    expect(siteBlockers(hot, NOW).map((b) => [b.kind, b.remainingMs])).toEqual([
      ['exclusive', 10_000],
      ['siteCooldown', 60_000],
    ]);
    expect(siteBlockers(undefined, NOW)).toEqual([]);
  });
});
