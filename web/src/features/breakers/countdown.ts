import { useSyncExternalStore } from 'react';

import { type Timestamp } from '@bufbuild/protobuf/wkt';

import { remainingMs, toDate } from '@/lib/time';

const SECOND = 1000;
const MINUTE = 60 * SECOND;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

function pad(n: number): string {
  return String(n).padStart(2, '0');
}

/**
 * Compact countdown for breaker open periods:
 * "45s", "4m 05s", "2h 03m 09s", "3d 04h 10m"; "0s" when elapsed.
 */
export function formatCountdown(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return '0s';
  const total = Math.ceil(ms / SECOND) * SECOND;
  const days = Math.floor(total / DAY);
  const hours = Math.floor((total % DAY) / HOUR);
  const minutes = Math.floor((total % HOUR) / MINUTE);
  const seconds = Math.floor((total % MINUTE) / SECOND);
  if (days > 0) return `${days}d ${pad(hours)}h ${pad(minutes)}m`;
  if (hours > 0) return `${hours}h ${pad(minutes)}m ${pad(seconds)}s`;
  if (minutes > 0) return `${minutes}m ${pad(seconds)}s`;
  return `${seconds}s`;
}

export type OpenPeriod =
  { kind: 'none' } | { kind: 'indefinite' } | { kind: 'until'; remainingMs: number; until: Date };

/** Describes the open period of a breaker state at `now`. */
export function openPeriod(state: string, openUntil: Timestamp | undefined, now: number): OpenPeriod {
  if (state !== 'open') return { kind: 'none' };
  const until = toDate(openUntil);
  if (!until) return { kind: 'indefinite' };
  return { kind: 'until', remainingMs: remainingMs(until, now), until };
}

/** Clamps a ratio to 0..1 (NaN becomes 0). */
export function clampRatio(value: number): number {
  if (!Number.isFinite(value) || value < 0) return 0;
  return value > 1 ? 1 : value;
}

// Shared one-second ticker for countdowns (one interval for all subscribers).
type Listener = () => void;
const listeners = new Set<Listener>();
let now = Date.now();
let timer: ReturnType<typeof setInterval> | undefined;

function subscribe(listener: Listener): () => void {
  listeners.add(listener);
  if (timer === undefined) {
    now = Date.now();
    timer = setInterval(() => {
      now = Date.now();
      listeners.forEach((l) => l());
    }, SECOND);
  }
  return () => {
    listeners.delete(listener);
    if (listeners.size === 0 && timer !== undefined) {
      clearInterval(timer);
      timer = undefined;
    }
  };
}

function snapshot(): number {
  if (timer === undefined) now = Math.floor(Date.now() / SECOND) * SECOND;
  return now;
}

/** Current time updated every second (for countdowns). */
export function useSecondTicker(): number {
  return useSyncExternalStore(subscribe, snapshot, snapshot);
}
