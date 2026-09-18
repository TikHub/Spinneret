import { type JsonObject } from '@bufbuild/protobuf';
import { useCallback, useEffect, useState } from 'react';

/** How long a revealed payload stays visible before it is masked again. */
export const REVEAL_DURATION_MS = 60_000;
const TICK_MS = 1000;

/** A revealed payload and the time it must be hidden again. */
export interface RevealState {
  /** Identity the payload belongs to. */
  identityId: string;
  payload: JsonObject;
  /** Epoch ms after which the payload is hidden. */
  expiresAt: number;
}

export function createRevealState(
  identityId: string,
  payload: JsonObject,
  now: number,
  durationMs: number = REVEAL_DURATION_MS,
): RevealState {
  return { identityId, payload, expiresAt: now + durationMs };
}

/** Milliseconds until the reveal expires (0 when none or expired). */
export function revealRemainingMs(state: RevealState | undefined, now: number): number {
  if (!state) return 0;
  return Math.max(0, state.expiresAt - now);
}

/** Whether the reveal is still visible for the given identity. */
export function isRevealActive(state: RevealState | undefined, identityId: string, now: number): boolean {
  return state !== undefined && state.identityId === identityId && revealRemainingMs(state, now) > 0;
}

/** Whole seconds left, rounded up (60, 59, ..., 1, then 0). */
export function revealSecondsLeft(state: RevealState | undefined, now: number): number {
  return Math.ceil(revealRemainingMs(state, now) / 1000);
}

export interface RevealedPayload {
  /** The revealed payload while active, otherwise undefined. */
  payload: JsonObject | undefined;
  secondsLeft: number;
  reveal: (payload: JsonObject) => void;
  hide: () => void;
}

/**
 * Keeps a revealed payload in component state (never in the query cache) and
 * drops it after REVEAL_DURATION_MS or when the identity changes.
 */
export function useRevealedPayload(identityId: string): RevealedPayload {
  const [state, setState] = useState<RevealState>();
  const [now, setNow] = useState(() => Date.now());

  const active = isRevealActive(state, identityId, now);

  useEffect(() => {
    if (!state) return undefined;
    const tick = () => {
      const current = Date.now();
      setNow(current);
      if (current >= state.expiresAt) setState(undefined);
    };
    const timer = setInterval(tick, TICK_MS);
    const expiry = setTimeout(tick, Math.max(0, state.expiresAt - Date.now()));
    return () => {
      clearInterval(timer);
      clearTimeout(expiry);
    };
  }, [state]);

  const reveal = useCallback(
    (payload: JsonObject) => {
      const current = Date.now();
      setNow(current);
      setState(createRevealState(identityId, payload, current));
    },
    [identityId],
  );
  const hide = useCallback(() => setState(undefined), []);

  return {
    payload: active ? state?.payload : undefined,
    secondsLeft: active ? revealSecondsLeft(state, now) : 0,
    reveal,
    hide,
  };
}
