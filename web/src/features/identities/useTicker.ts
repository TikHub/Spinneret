import { useSyncExternalStore } from 'react';

/** Tick interval of countdowns. */
export const TICKER_MS = 1000;

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
    }, TICKER_MS);
  }
  return () => {
    listeners.delete(listener);
    if (listeners.size === 0 && timer !== undefined) {
      clearInterval(timer);
      timer = undefined;
    }
  };
}

function getSnapshot(): number {
  // Without subscribers the interval is stopped; second precision keeps repeated reads stable.
  if (timer === undefined) now = Math.floor(Date.now() / TICKER_MS) * TICKER_MS;
  return now;
}

function subscribeNoop(): () => void {
  return () => undefined;
}

/** Current time in epoch ms, updated every second while `enabled` (shared by all countdowns). */
export function useSecondTicker(enabled = true): number {
  return useSyncExternalStore(enabled ? subscribe : subscribeNoop, getSnapshot, getSnapshot);
}
