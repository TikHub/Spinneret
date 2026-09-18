import { act, renderHook } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import {
  createRevealState,
  isRevealActive,
  REVEAL_DURATION_MS,
  revealRemainingMs,
  revealSecondsLeft,
  useRevealedPayload,
} from './reveal';

describe('reveal timer logic', () => {
  const now = 1_000_000;
  const state = createRevealState('idt_1', { token: 'secret' }, now);

  it('expires after the reveal duration', () => {
    expect(state.expiresAt).toBe(now + REVEAL_DURATION_MS);
    expect(revealRemainingMs(state, now)).toBe(60_000);
    expect(revealSecondsLeft(state, now + 500)).toBe(60);
    expect(revealSecondsLeft(state, now + 59_001)).toBe(1);
    expect(revealSecondsLeft(state, now + 60_000)).toBe(0);
    expect(revealRemainingMs(state, now + 70_000)).toBe(0);
    expect(revealRemainingMs(undefined, now)).toBe(0);
  });

  it('is only active for the same identity and before expiry', () => {
    expect(isRevealActive(state, 'idt_1', now + 1)).toBe(true);
    expect(isRevealActive(state, 'idt_2', now + 1)).toBe(false);
    expect(isRevealActive(state, 'idt_1', now + REVEAL_DURATION_MS)).toBe(false);
    expect(isRevealActive(undefined, 'idt_1', now)).toBe(false);
  });
});

describe('useRevealedPayload', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-01-01T00:00:00Z'));
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it('shows the payload for 60 seconds, counts down and re-masks', () => {
    const { result } = renderHook(() => useRevealedPayload('idt_1'));
    expect(result.current.payload).toBeUndefined();

    act(() => result.current.reveal({ cookie: 'abc' }));
    expect(result.current.payload).toEqual({ cookie: 'abc' });
    expect(result.current.secondsLeft).toBe(60);

    act(() => {
      vi.advanceTimersByTime(30_000);
    });
    expect(result.current.secondsLeft).toBe(30);
    expect(result.current.payload).toEqual({ cookie: 'abc' });

    act(() => {
      vi.advanceTimersByTime(30_000);
    });
    expect(result.current.payload).toBeUndefined();
    expect(result.current.secondsLeft).toBe(0);
  });

  it('hides on demand and when the identity changes', () => {
    const { result, rerender } = renderHook(({ id }) => useRevealedPayload(id), {
      initialProps: { id: 'idt_1' },
    });
    act(() => result.current.reveal({ a: 1 }));
    rerender({ id: 'idt_2' });
    expect(result.current.payload).toBeUndefined();

    act(() => result.current.reveal({ b: 2 }));
    expect(result.current.payload).toEqual({ b: 2 });
    act(() => result.current.hide());
    expect(result.current.payload).toBeUndefined();
  });
});
