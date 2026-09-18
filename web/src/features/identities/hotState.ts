import { type EndpointHotState, type IdentityHotState } from '@/gen/spinneret/v1/identity_admin_pb';
import { DAY_MS, HOUR_MS, MINUTE_MS, remainingMs, SECOND_MS, toDate, type TimeInput } from '@/lib/time';

export type ScoreTone = 'good' | 'warn' | 'bad';

/** Score at or above which an identity is considered healthy. */
export const SCORE_GOOD = 60;
/** Score below which an identity is considered unhealthy. */
export const SCORE_BAD = 30;

/** Clamps a health score to 0..100 (NaN becomes 0). */
export function clampScore(score: number): number {
  if (!Number.isFinite(score)) return 0;
  return Math.min(100, Math.max(0, score));
}

export function scoreTone(score: number): ScoreTone {
  const value = clampScore(score);
  if (value >= SCORE_GOOD) return 'good';
  if (value >= SCORE_BAD) return 'warn';
  return 'bad';
}

/** Score with one decimal at most ("72.5", "80"). */
export function formatScore(score: number): string {
  const value = clampScore(score);
  return Number.isInteger(value) ? String(value) : value.toFixed(1);
}

export type CountdownUnit = 'd' | 'h' | 'm' | 's';

export interface CountdownPart {
  unit: CountdownUnit;
  value: number;
}

const COUNTDOWN_UNITS: ReadonlyArray<[CountdownUnit, number]> = [
  ['d', DAY_MS],
  ['h', HOUR_MS],
  ['m', MINUTE_MS],
  ['s', SECOND_MS],
];

/** Largest non-zero units of the remaining time (at most maxUnits); empty when unset or past. */
export function countdownParts(until: TimeInput, now: number, maxUnits = 2): CountdownPart[] {
  const ms = remainingMs(until, now);
  if (ms <= 0) return [];
  // Round sub-second remainders up so a running countdown never shows "0s".
  let rest = Math.max(Math.floor(ms / SECOND_MS), 1) * SECOND_MS;
  const parts: CountdownPart[] = [];
  for (const [unit, size] of COUNTDOWN_UNITS) {
    const value = Math.floor(rest / size);
    if (value > 0) {
      parts.push({ unit, value });
      rest -= value * size;
    }
    if (parts.length >= maxUnits) break;
  }
  return parts;
}

/**
 * Remaining time as "4m 12s" / "1d 2h" (or with localized units through
 * formatPart); empty when the time is unset or in the past.
 */
export function formatCountdown(
  until: TimeInput,
  now: number,
  formatPart: (part: CountdownPart) => string = (part) => `${part.value}${part.unit}`,
): string {
  return countdownParts(until, now).map(formatPart).join(' ');
}

export type EndpointAvailability = 'ready' | 'queued' | 'cooldown' | 'reuse' | 'waiting';

/**
 * Availability of an identity for one endpoint group: cooldown and reuse
 * intervals take precedence, then a future available_at; otherwise the
 * identity is ready (in the ready queue) or merely eligible (not queued).
 */
export function endpointAvailability(group: EndpointHotState, now: number): EndpointAvailability {
  if (remainingMs(group.cooldownUntil, now) > 0) return 'cooldown';
  if (remainingMs(group.reuseUntil, now) > 0) return 'reuse';
  if (remainingMs(group.availableAt, now) > 0) return 'waiting';
  return group.inReadyQueue ? 'ready' : 'queued';
}

/** Endpoint groups sorted by client, then name. */
export function sortEndpointGroups(groups: readonly EndpointHotState[]): EndpointHotState[] {
  return [...groups].sort(
    (a, b) => a.client.localeCompare(b.client) || a.endpointGroup.localeCompare(b.endpointGroup),
  );
}

export type SiteBlockerKind = 'siteCooldown' | 'siteReuse' | 'exclusive' | 'accountCooldown';

export interface SiteBlocker {
  kind: SiteBlockerKind;
  until: Date;
  remainingMs: number;
}

/** Site-level intervals that currently block leasing, soonest end first. */
export function siteBlockers(hot: IdentityHotState | undefined, now: number): SiteBlocker[] {
  if (!hot) return [];
  const candidates: Array<[SiteBlockerKind, TimeInput]> = [
    ['siteCooldown', hot.siteCooldownUntil],
    ['siteReuse', hot.siteReuseUntil],
    ['exclusive', hot.exclusiveUntil],
    ['accountCooldown', hot.accountCooldownUntil],
  ];
  const out: SiteBlocker[] = [];
  for (const [kind, value] of candidates) {
    const until = toDate(value);
    const ms = remainingMs(value, now);
    if (until && ms > 0) out.push({ kind, until, remainingMs: ms });
  }
  return out.sort((a, b) => a.remainingMs - b.remainingMs);
}
