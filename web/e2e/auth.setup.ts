import { expect, test as setup } from '@playwright/test';

import { ADMIN_PASSWORD, ADMIN_USER, NAMESPACE, STORAGE_STATE } from './fixtures/env';

/**
 * Signs in through the UI once per run and stores the session cookie together
 * with the console preferences (English, light theme, default scope) so every
 * spec starts from the same deterministic state.
 */
setup('sign in as the console administrator', async ({ page }) => {
  await page.addInitScript(() => {
    window.localStorage.setItem('spinneret.lang', 'en');
    window.localStorage.setItem('spinneret.theme', 'light');
    window.localStorage.setItem('spinneret.sidebar.collapsed', 'false');
  });

  await page.goto('/login');
  await page.getByLabel('Username').fill(ADMIN_USER);
  await page.getByLabel('Password').fill(ADMIN_PASSWORD);
  await page.getByRole('button', { name: 'Sign in' }).click();

  await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible();

  // Pin the scope the suite works in.
  await page.evaluate((namespace) => {
    window.localStorage.setItem('spinneret.namespace', namespace);
  }, NAMESPACE);

  await page.context().storageState({ path: STORAGE_STATE });
});
