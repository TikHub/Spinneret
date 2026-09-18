import '@testing-library/jest-dom/vitest';
import { cleanup } from '@testing-library/react';
import { afterEach } from 'vitest';

/**
 * Browser APIs jsdom lacks but Radix primitives (popper, select, switch,
 * checkbox), ECharts and the theme provider call. Each stub is installed only
 * when the API is missing, so a future jsdom implementation wins.
 */
function installDomStubs(): void {
  if (typeof globalThis.ResizeObserver === 'undefined') {
    class ResizeObserverStub implements ResizeObserver {
      observe(): void {}
      unobserve(): void {}
      disconnect(): void {}
    }
    Object.defineProperty(globalThis, 'ResizeObserver', {
      value: ResizeObserverStub,
      writable: true,
      configurable: true,
    });
  }

  const element = Element.prototype;
  element.hasPointerCapture ??= () => false;
  element.setPointerCapture ??= () => undefined;
  element.releasePointerCapture ??= () => undefined;
  element.scrollIntoView ??= () => undefined;

  if (typeof window.matchMedia !== 'function') {
    Object.defineProperty(window, 'matchMedia', {
      writable: true,
      configurable: true,
      value: (query: string): MediaQueryList => {
        const target = new EventTarget();
        return Object.assign(target, {
          matches: false,
          media: query,
          onchange: null,
          addListener: () => undefined,
          removeListener: () => undefined,
        }) as MediaQueryList;
      },
    });
  }

  // jsdom's scrollTo only logs "Not implemented"; the router calls it on navigation.
  Object.defineProperty(window, 'scrollTo', { writable: true, configurable: true, value: () => undefined });
}

installDomStubs();

afterEach(() => {
  cleanup();
});
