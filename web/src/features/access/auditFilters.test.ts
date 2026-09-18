import { timestampMs } from '@bufbuild/protobuf/wkt';
import { describe, expect, it } from 'vitest';

import { DAY_MS, HOUR_MS } from '@/lib/time';

import {
  activeAuditFilterCount,
  DEFAULT_AUDIT_FILTERS,
  parseAuditSearch,
  parseLocalDateTime,
  resolveAuditTimeRange,
  toAuditSearch,
  toListAuditLogsInit,
} from './auditFilters';

describe('audit filters and URL search', () => {
  it('uses defaults for empty or invalid search params', () => {
    expect(parseAuditSearch({})).toEqual(DEFAULT_AUDIT_FILTERS);
    expect(parseAuditSearch({ result: 'maybe', range: 'forever', from: 'x' })).toEqual(DEFAULT_AUDIT_FILTERS);
  });

  it('coerces numeric params and truncates long values', () => {
    const filters = parseAuditSearch({ resourceId: 12345, actor: 'a'.repeat(300) });
    expect(filters.resourceId).toBe('12345');
    expect(filters.actor).toHaveLength(128);
  });

  it('round-trips through search params without default values', () => {
    const filters = {
      ...DEFAULT_AUDIT_FILTERS,
      namespace: 'prod',
      actor: 'ops',
      action: 'secret.read',
      resourceKind: 'secret',
      resourceId: 'sec_1',
      result: 'denied' as const,
      range: 'custom' as const,
      from: '2026-09-01T08:00',
      to: '2026-09-02T08:00',
    };
    const search = toAuditSearch(filters);
    expect(parseAuditSearch(search)).toEqual(filters);
    expect(toAuditSearch(DEFAULT_AUDIT_FILTERS)).toEqual({
      namespace: undefined,
      actor: undefined,
      action: undefined,
      resourceKind: undefined,
      resourceId: undefined,
      result: undefined,
      range: undefined,
      from: undefined,
      to: undefined,
    });
    expect(activeAuditFilterCount(filters)).toBe(7);
    expect(activeAuditFilterCount(DEFAULT_AUDIT_FILTERS)).toBe(0);
  });

  it('drops custom bounds for preset ranges', () => {
    const search = toAuditSearch({ ...DEFAULT_AUDIT_FILTERS, range: '7d', from: '2026-09-01T08:00' });
    expect(search.range).toBe('7d');
    expect(search.from).toBeUndefined();
    expect(parseAuditSearch({ range: '7d', from: '2026-09-01T08:00' }).from).toBe('');
  });
});

describe('audit time range', () => {
  const now = Date.UTC(2026, 8, 17, 12, 0, 0);

  it('parses datetime-local values', () => {
    expect(parseLocalDateTime('2026-09-01T08:30')?.getHours()).toBe(8);
    expect(parseLocalDateTime('2026-02-30T08:30')).toBeUndefined();
    expect(parseLocalDateTime('yesterday')).toBeUndefined();
  });

  it('resolves presets relative to now', () => {
    expect(resolveAuditTimeRange({ ...DEFAULT_AUDIT_FILTERS, range: '1h' }, now)).toEqual({
      startMs: now - HOUR_MS,
      endMs: now,
    });
    expect(resolveAuditTimeRange(DEFAULT_AUDIT_FILTERS, now)).toEqual({ startMs: now - DAY_MS, endMs: now });
  });

  it('rejects custom ranges whose end is not after the start', () => {
    const filters = {
      ...DEFAULT_AUDIT_FILTERS,
      range: 'custom' as const,
      from: '2026-09-02T08:00',
      to: '2026-09-01T08:00',
    };
    expect(resolveAuditTimeRange(filters, now)).toBe('invalid');
    expect(resolveAuditTimeRange({ ...filters, to: '' }, now)).toEqual({
      startMs: parseLocalDateTime('2026-09-02T08:00')?.getTime(),
      endMs: undefined,
    });
  });

  it('builds the request with trimmed filters and a time range', () => {
    const init = toListAuditLogsInit({ ...DEFAULT_AUDIT_FILTERS, actor: ' ops ', result: 'ok' }, now);
    expect(init).toMatchObject({ actor: 'ops', result: 'ok', namespace: '' });
    expect(init.timeRange?.start && timestampMs(init.timeRange.start)).toBe(now - DAY_MS);
    // Presets leave the end open so the server clock decides what "now" is.
    expect(init.timeRange?.end).toBeUndefined();

    const open = toListAuditLogsInit({ ...DEFAULT_AUDIT_FILTERS, range: 'custom' }, now);
    expect(open.timeRange?.start && timestampMs(open.timeRange.start)).toBe(0);
    expect(open.timeRange?.end).toBeUndefined();
  });

  it('keeps custom bounds and opens a missing start at the epoch', () => {
    const custom = { ...DEFAULT_AUDIT_FILTERS, range: 'custom' as const };
    const to = '2026-08-01T08:00';
    const endOnly = toListAuditLogsInit({ ...custom, to }, now);
    expect(endOnly.timeRange?.start && timestampMs(endOnly.timeRange.start)).toBe(0);
    expect(endOnly.timeRange?.end && timestampMs(endOnly.timeRange.end)).toBe(
      parseLocalDateTime(to)?.getTime(),
    );

    const from = '2026-07-01T08:00';
    const both = toListAuditLogsInit({ ...custom, from, to }, now);
    expect(both.timeRange?.start && timestampMs(both.timeRange.start)).toBe(
      parseLocalDateTime(from)?.getTime(),
    );
    expect(both.timeRange?.end && timestampMs(both.timeRange.end)).toBe(parseLocalDateTime(to)?.getTime());

    expect(toListAuditLogsInit({ ...custom, from: to, to: from }, now).timeRange).toBeUndefined();
  });
});
