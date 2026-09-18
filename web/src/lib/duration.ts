/**
 * Duration strings shared with the backend (internal/pkg/durationx and the
 * admin API validation pattern): an optional leading "<n>d" followed by
 * "<number><unit>" parts with units ms|s|m|h, e.g. "30s", "10m", "1h30m",
 * "7d", "1d12h", "1.5h", plus "0", "" (zero) and the keyword "permanent".
 */

import { type Translate } from '@/lib/errors';

export const PERMANENT = 'permanent';

export const DURATION_PATTERN = /^(permanent|0|([0-9]+d)?([0-9]+(\.[0-9]+)?(ms|s|m|h))*)$/;

const UNIT_MS = { ms: 1, s: 1000, m: 60_000, h: 3_600_000 } as const;
const DAY_MS = 86_400_000;

type Unit = keyof typeof UNIT_MS;

/** Result of parsing a duration string. */
export type ParsedDuration = { permanent: true; ms: -1 } | { permanent: false; ms: number };

export const PERMANENT_DURATION: ParsedDuration = { permanent: true, ms: -1 };

/** Parses a duration string; returns undefined when the string is invalid. */
export function parseDuration(input: string): ParsedDuration | undefined {
  const s = input.trim();
  if (s === '' || s === '0') return { permanent: false, ms: 0 };
  if (s.toLowerCase() === PERMANENT) return PERMANENT_DURATION;

  let total = 0;
  let rest = s;
  const day = /^(\d+)d/.exec(rest);
  if (day) {
    total += Number(day[1]) * DAY_MS;
    rest = rest.slice(day[0].length);
  }
  const part = /(\d+(?:\.\d+)?)(ms|s|m|h)/y;
  let pos = 0;
  while (pos < rest.length) {
    part.lastIndex = pos;
    const m = part.exec(rest);
    if (!m) return undefined;
    total += Number(m[1]) * UNIT_MS[m[2] as Unit];
    pos = part.lastIndex;
  }
  if (!Number.isFinite(total) || total > Number.MAX_SAFE_INTEGER) return undefined;
  return { permanent: false, ms: Math.round(total) };
}

/** Reports whether the string is a valid duration; `allowPermanent` controls the keyword. */
export function isValidDuration(input: string, allowPermanent = true): boolean {
  const parsed = parseDuration(input);
  if (!parsed) return false;
  return allowPermanent || !parsed.permanent;
}

/** Duration in milliseconds (-1 for permanent), or undefined when invalid. */
export function durationToMs(input: string): number | undefined {
  return parseDuration(input)?.ms;
}

/**
 * Canonical representation matching durationx.Duration.String():
 * "0s", "250ms", "30s", "1h30m", "1d12h", "permanent" (for -1 or a permanent value).
 */
export function formatDuration(value: number | ParsedDuration): string {
  const ms = typeof value === 'number' ? value : value.ms;
  if ((typeof value !== 'number' && value.permanent) || ms === -1) return PERMANENT;
  if (ms === 0) return '0s';
  if (ms < 0) return `${ms}ms`;
  let rest = Math.round(ms);
  let out = '';
  const units: Array<[string, number]> = [
    ['d', DAY_MS],
    ['h', UNIT_MS.h],
    ['m', UNIT_MS.m],
    ['s', UNIT_MS.s],
  ];
  for (const [unit, size] of units) {
    const n = Math.floor(rest / size);
    if (n > 0) {
      out += `${n}${unit}`;
      rest -= n * size;
    }
  }
  if (rest > 0) out += `${rest}ms`;
  return out;
}

/** Normalizes a valid duration string to its canonical form; undefined when invalid. */
export function normalizeDuration(input: string): string | undefined {
  const parsed = parseDuration(input);
  return parsed ? formatDuration(parsed) : undefined;
}

/** Options of humanizeDuration. */
export interface HumanizeDurationOptions {
  /** Largest units kept (default 2): 1d 2h 3m becomes "1d 2h". */
  maxUnits?: number;
  /**
   * Translation function (any namespace; keys are read from common). Units then
   * come from `duration.units.*` and -1 from `duration.permanent`; without it the
   * output is the compact English form.
   */
  t?: Translate;
}

const HUMANIZE_UNITS = [
  { key: 'days', symbol: 'd', size: DAY_MS },
  { key: 'hours', symbol: 'h', size: UNIT_MS.h },
  { key: 'minutes', symbol: 'm', size: UNIT_MS.m },
  { key: 'seconds', symbol: 's', size: UNIT_MS.s },
] as const;

type HumanizeUnitKey = (typeof HUMANIZE_UNITS)[number]['key'] | 'milliseconds';

const UNIT_SYMBOLS: Record<HumanizeUnitKey, string> = {
  days: 'd',
  hours: 'h',
  minutes: 'm',
  seconds: 's',
  milliseconds: 'ms',
};

function unitText(count: number, unit: HumanizeUnitKey, t: Translate | undefined): string {
  return t ? t(`common:duration.units.${unit}`, { count }) : `${count}${UNIT_SYMBOLS[unit]}`;
}

/**
 * Short human form with at most `maxUnits` units, e.g. "1d 2h", "3m 20s", "250ms".
 * Returns "permanent" for -1 and "0s" for zero. Pass `{ t }` for the viewer's
 * language ("1天 2小时", "永久"); a number second argument is `maxUnits`.
 */
export function humanizeDuration(ms: number, options: number | HumanizeDurationOptions = {}): string {
  const { maxUnits = 2, t } = typeof options === 'number' ? { maxUnits: options } : options;
  if (ms === -1) return t ? t('common:duration.permanent') : PERMANENT;
  if (ms <= 0) return unitText(0, 'seconds', t);
  if (ms < 1000) return unitText(Math.round(ms), 'milliseconds', t);
  const parts: string[] = [];
  let rest = Math.floor(ms / 1000) * 1000;
  for (const { key, size } of HUMANIZE_UNITS) {
    const n = Math.floor(rest / size);
    if (n > 0) {
      parts.push(unitText(n, key, t));
      rest -= n * size;
    }
    if (parts.length >= maxUnits) break;
  }
  return parts.join(' ');
}

/** Common presets offered by DurationInput. */
export const DURATION_PRESETS = ['30s', '5m', '10m', '30m', '1h', '6h', '1d', '7d', '30d'] as const;

/** Validation result of a duration input value. */
export function validateDurationInput(
  value: string,
  { allowPermanent = false, allowEmpty = false }: { allowPermanent?: boolean; allowEmpty?: boolean } = {},
): 'ok' | 'empty' | 'invalid' | 'permanent' {
  if (value.trim() === '') return allowEmpty ? 'ok' : 'empty';
  const parsed = parseDuration(value);
  if (!parsed) return 'invalid';
  if (parsed.permanent && !allowPermanent) return 'permanent';
  return 'ok';
}
