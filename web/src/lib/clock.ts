import { useSyncExternalStore } from 'react';

/** Tick interval shared by relative-time displays. */
export const CLOCK_TICK_MS = 15_000;

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
    }, CLOCK_TICK_MS);
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
  // Without subscribers the interval is stopped; refresh (second precision keeps
  // consecutive snapshot reads stable) so the first render is never stale.
  if (timer === undefined) {
    now = Math.floor(Date.now() / 1000) * 1000;
  }
  return now;
}

/** Current time in epoch ms, updated every CLOCK_TICK_MS for all subscribers at once. */
export function useNow(): number {
  return useSyncExternalStore(subscribe, getSnapshot, getSnapshot);
}
