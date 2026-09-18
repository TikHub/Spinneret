import { useCallback, useEffect, useState } from 'react';

import { REVEAL_VISIBLE_MS, revealSecondsLeft } from './secretTime';

const TICK_MS = 250;

/** A revealed secret value. */
export interface RevealedSecret {
  value: string;
  version: number;
}

interface RevealState {
  secret: RevealedSecret;
  hideAt: number;
}

export interface RevealTimer {
  /** The revealed value while visible. */
  revealed: RevealedSecret | undefined;
  /** Epoch ms at which the value is hidden (undefined while hidden). */
  hideAt: number | undefined;
  /** How long a revealed value stays visible. */
  durationMs: number;
  show: (secret: RevealedSecret) => void;
  hide: () => void;
}

/**
 * Keeps a revealed secret in component state only (never in the query cache)
 * and drops it after `visibleMs`, when hidden explicitly or on unmount. It
 * does not tick: the page holding it re-renders only on show and hide; use
 * useRevealCountdown where the countdown is displayed.
 */
export function useRevealTimer(visibleMs: number = REVEAL_VISIBLE_MS): RevealTimer {
  const [state, setState] = useState<RevealState>();

  const show = useCallback(
    (secret: RevealedSecret) => setState({ secret, hideAt: Date.now() + visibleMs }),
    [visibleMs],
  );
  const hide = useCallback(() => setState(undefined), []);

  useEffect(() => {
    if (!state) return undefined;
    const timeout = setTimeout(() => setState(undefined), Math.max(0, state.hideAt - Date.now()));
    return () => clearTimeout(timeout);
  }, [state]);

  return { revealed: state?.secret, hideAt: state?.hideAt, durationMs: visibleMs, show, hide };
}

/** Whole seconds until a revealed value is hidden (0 while hidden), refreshed a few times per second. */
export function useRevealCountdown(
  hideAt: number | undefined,
  durationMs: number = REVEAL_VISIBLE_MS,
): number {
  const [tick, setTick] = useState<{ hideAt: number; now: number }>();

  useEffect(() => {
    if (hideAt === undefined) return undefined;
    const interval = setInterval(() => setTick({ hideAt, now: Date.now() }), TICK_MS);
    return () => clearInterval(interval);
  }, [hideAt]);

  if (hideAt === undefined) return 0;
  // Until the first tick of this reveal the whole duration is left.
  const now = tick?.hideAt === hideAt ? tick.now : hideAt - durationMs;
  return revealSecondsLeft(hideAt, now);
}
