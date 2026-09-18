import { act, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import '@/i18n';
import { TooltipProvider } from '@/components/ui/tooltip';

import { Countdown } from './Countdown';

const START = new Date('2026-01-01T00:00:00Z').getTime();

function view(until: Date | undefined) {
  return (
    <TooltipProvider>
      <span data-testid="countdown">
        <Countdown until={until} />
      </span>
    </TooltipProvider>
  );
}

const text = () => screen.getByTestId('countdown').textContent;

describe('Countdown', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(START);
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it('ticks down and shows the fallback once the end has passed', () => {
    render(view(new Date(START + 3_000)));
    expect(text()).toBe('3s');
    act(() => void vi.advanceTimersByTime(1_000));
    expect(text()).toBe('2s');
    act(() => void vi.advanceTimersByTime(2_000));
    expect(text()).toBe('—');
  });

  it('measures a new end time against the current clock after an earlier countdown ended', () => {
    const { rerender } = render(view(new Date(START + 2_000)));
    act(() => void vi.advanceTimersByTime(2_000));
    expect(text()).toBe('—');

    // Five minutes later a new 10 minute cooldown arrives.
    vi.setSystemTime(START + 302_000);
    rerender(view(new Date(START + 302_000 + 600_000)));
    expect(text()).toBe('10m');

    // An end time that is already over when it arrives never shows time left.
    rerender(view(new Date(START + 301_000)));
    expect(text()).toBe('—');
  });
});
