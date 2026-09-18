import { act, renderHook } from '@testing-library/react';
import { useState } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { DAY_MS, HOUR_MS } from '@/lib/time';

import { expiryState, parseDateTimeLocal, revealSecondsLeft, toDateTimeLocal } from './secretTime';
import { useRevealCountdown, useRevealTimer } from './useRevealTimer';

/** The timer plus the countdown shown next to the value. */
function useTimerWithCountdown(visibleMs?: number) {
  const timer = useRevealTimer(visibleMs);
  const secondsLeft = useRevealCountdown(timer.hideAt, timer.durationMs);
  return { ...timer, secondsLeft };
}

describe('expiryState', () => {
  const now = Date.UTC(2026, 8, 17, 12);

  it('classifies expiry times', () => {
    expect(expiryState(undefined, now)).toBe('none');
    expect(expiryState(now - 1, now)).toBe('expired');
    expect(expiryState(now + HOUR_MS, now)).toBe('soon');
    expect(expiryState(now + 7 * DAY_MS, now)).toBe('soon');
    expect(expiryState(now + 7 * DAY_MS + 1, now)).toBe('ok');
  });
});

describe('datetime-local conversion', () => {
  it('round-trips local times', () => {
    const date = new Date(2026, 0, 2, 3, 4);
    const text = toDateTimeLocal(date);
    expect(text).toBe('2026-01-02T03:04');
    expect(parseDateTimeLocal(text)?.getTime()).toBe(date.getTime());
  });

  it('rejects malformed values', () => {
    expect(parseDateTimeLocal('')).toBeUndefined();
    expect(parseDateTimeLocal('2026-01-02')).toBeUndefined();
    expect(toDateTimeLocal(undefined)).toBe('');
  });
});

describe('reveal timer', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date(2026, 8, 17, 12, 0, 0));
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it('computes whole seconds left', () => {
    expect(revealSecondsLeft(10_000, 0)).toBe(10);
    expect(revealSecondsLeft(10_000, 9_001)).toBe(1);
    expect(revealSecondsLeft(10_000, 12_000)).toBe(0);
  });

  it('shows the value for 60 seconds and then drops it', () => {
    const { result } = renderHook(() => useTimerWithCountdown());
    expect(result.current.revealed).toBeUndefined();

    act(() => result.current.show({ value: 's3cr3t', version: 2 }));
    expect(result.current.revealed).toEqual({ value: 's3cr3t', version: 2 });
    expect(result.current.secondsLeft).toBe(60);

    act(() => {
      vi.advanceTimersByTime(30_000);
    });
    expect(result.current.revealed?.value).toBe('s3cr3t');
    expect(result.current.secondsLeft).toBe(30);

    act(() => {
      vi.advanceTimersByTime(30_000);
    });
    expect(result.current.revealed).toBeUndefined();
    expect(result.current.secondsLeft).toBe(0);
  });

  it('hides on demand and restarts the countdown on a new reveal', () => {
    const { result } = renderHook(() => useTimerWithCountdown(5_000));
    act(() => result.current.show({ value: 'a', version: 1 }));
    act(() => {
      vi.advanceTimersByTime(4_000);
    });
    act(() => result.current.show({ value: 'b', version: 2 }));
    act(() => {
      vi.advanceTimersByTime(4_000);
    });
    expect(result.current.revealed?.value).toBe('b');
    act(() => result.current.hide());
    expect(result.current.revealed).toBeUndefined();
    expect(result.current.secondsLeft).toBe(0);
  });

  it('does not re-render the holder of the timer while counting down', () => {
    let renders = 0;
    const { result } = renderHook(() => {
      renders++;
      // A state setter keeps the hook shape of a real page component.
      useState(0);
      return useRevealTimer(10_000);
    });
    act(() => result.current.show({ value: 'x', version: 1 }));
    const afterShow = renders;
    act(() => {
      vi.advanceTimersByTime(9_000);
    });
    expect(renders).toBe(afterShow);
    act(() => {
      vi.advanceTimersByTime(1_000);
    });
    expect(result.current.revealed).toBeUndefined();
  });
});
