import { describe, expect, it } from 'vitest';

describe('test setup DOM stubs', () => {
  it('provides a ResizeObserver that can observe and disconnect', () => {
    expect(typeof globalThis.ResizeObserver).toBe('function');
    const observer = new ResizeObserver(() => undefined);
    const element = document.createElement('div');
    expect(() => {
      observer.observe(element);
      observer.unobserve(element);
      observer.disconnect();
    }).not.toThrow();
  });

  it('provides the pointer capture and scrolling APIs Radix primitives call', () => {
    const element = document.createElement('div');
    expect(element.hasPointerCapture(1)).toBe(false);
    expect(() => element.setPointerCapture(1)).not.toThrow();
    expect(() => element.releasePointerCapture(1)).not.toThrow();
    expect(() => element.scrollIntoView()).not.toThrow();
  });

  it('provides a matchMedia that never matches and accepts listeners', () => {
    expect(typeof window.matchMedia).toBe('function');
    const media = window.matchMedia('(prefers-color-scheme: dark)');
    expect(media.matches).toBe(false);
    expect(media.media).toBe('(prefers-color-scheme: dark)');
    const listener = () => undefined;
    expect(() => {
      media.addEventListener('change', listener);
      media.removeEventListener('change', listener);
      media.addListener(listener);
      media.removeListener(listener);
    }).not.toThrow();
  });
});
