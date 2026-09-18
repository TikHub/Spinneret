import { expect, test as base, type Page } from '@playwright/test';

import { Api } from './api';

export { expect };

/**
 * Console messages that are expected while a test navigates away from a page
 * with in-flight requests, or that come from the browser rather than the app.
 * Everything else fails the test.
 */
const BENIGN_CONSOLE = [
  // React Query and Connect abort in-flight requests when a page unmounts; the
  // browser logs the cancelled fetch itself.
  /Failed to load resource: net::ERR_ABORTED/i,
  /The user aborted a request/i,
  /signal is aborted without reason/i,
  // Chromium logs every non-2xx fetch response; the expected ones (sign-in with
  // a wrong password, a page a restricted user may not read) are asserted in the
  // UI instead.
  /Failed to load resource: the server responded with a status of 4\d\d/i,
];

function isBenign(text: string): boolean {
  return BENIGN_CONSOLE.some((pattern) => pattern.test(text));
}

/** Browser console output collected during one test (benign messages excluded). */
interface ConsoleGuard {
  readonly messages: string[];
  /** Ignores messages matching this pattern for the rest of the test. */
  allow(pattern: RegExp): void;
}

interface Fixtures {
  /** Page that fails the test on any uncaught error or console.error. */
  page: Page;
  consoleGuard: ConsoleGuard;
  /** Connect client signed in as the console administrator (setup and cleanup). */
  api: Api;
}

export const test = base.extend<Fixtures>({
  consoleGuard: [
    // eslint-disable-next-line no-empty-pattern
    async ({}, use) => {
      const allowed: RegExp[] = [];
      const messages: string[] = [];
      await use({ messages, allow: (pattern: RegExp) => allowed.push(pattern) });
      const unexpected = messages.filter((m) => !allowed.some((p) => p.test(m)));
      expect(unexpected, `unexpected browser console output:\n${unexpected.join('\n')}`).toEqual([]);
    },
    { auto: true },
  ],

  page: async ({ page, consoleGuard }, use) => {
    page.on('pageerror', (error) => {
      consoleGuard.messages.push(`[pageerror] ${error.message}`);
    });
    page.on('console', (msg) => {
      if (msg.type() !== 'error') return;
      const text = msg.text();
      if (isBenign(text)) return;
      consoleGuard.messages.push(`[console.error] ${text}`);
    });
    await use(page);
  },

  // eslint-disable-next-line no-empty-pattern
  api: async ({}, use) => {
    const api = await Api.login();
    await use(api);
    await api.dispose();
  },
});
