import { format } from 'date-fns';

import { DAY_MS, SECOND_MS, toDate, type TimeInput } from '@/lib/time';

/** Secrets expiring within this window are highlighted (alerts fire 7 days before expiry). */
export const EXPIRY_WARNING_MS = 7 * DAY_MS;

export type ExpiryState = 'none' | 'ok' | 'soon' | 'expired';

/** Expiry classification of a secret. */
export function expiryState(expiresAt: TimeInput, now: number): ExpiryState {
  const date = toDate(expiresAt);
  if (!date) return 'none';
  const remaining = date.getTime() - now;
  if (remaining <= 0) return 'expired';
  return remaining <= EXPIRY_WARNING_MS ? 'soon' : 'ok';
}

/** Value for <input type="datetime-local"> in local time ("yyyy-MM-ddTHH:mm"). */
export function toDateTimeLocal(value: TimeInput): string {
  const date = toDate(value);
  return date ? format(date, "yyyy-MM-dd'T'HH:mm") : '';
}

/** Parses a datetime-local value (local time); undefined when empty or invalid. */
export function parseDateTimeLocal(value: string): Date | undefined {
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2})?$/.test(value)) return undefined;
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? undefined : date;
}

/** How long a revealed secret value stays visible. */
export const REVEAL_VISIBLE_MS = 60 * SECOND_MS;

/** Whole seconds left until `hideAt` (never negative). */
export function revealSecondsLeft(hideAt: number, now: number): number {
  return Math.max(0, Math.ceil((hideAt - now) / SECOND_MS));
}
