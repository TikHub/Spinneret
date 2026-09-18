import { describe, expect, it } from 'vitest';

import { computeBackoff, parseSseData } from './sse';

describe('computeBackoff', () => {
  it('grows exponentially within [base/2, base] and caps at max', () => {
    expect(computeBackoff(0, 1000, 30000, () => 0)).toBe(500);
    expect(computeBackoff(0, 1000, 30000, () => 1)).toBe(1000);
    expect(computeBackoff(3, 1000, 30000, () => 1)).toBe(8000);
    expect(computeBackoff(10, 1000, 30000, () => 1)).toBe(30000);
    expect(computeBackoff(10, 1000, 30000, () => 0)).toBe(15000);
  });
});

describe('parseSseData', () => {
  it('parses JSON and falls back to raw text', () => {
    expect(parseSseData('{"type":"alert"}')).toEqual({ type: 'alert' });
    expect(parseSseData('ping')).toBe('ping');
  });
});
