import { ADMIN_PASSWORD, ADMIN_USER } from './fixtures/env';
import { expect, test } from './fixtures/test';
import { dialog, dismissToasts, gotoPage } from './fixtures/ui';

test.describe('sign in and out', () => {
  // These journeys start without a session.
  test.use({ storageState: { cookies: [], origins: [] } });

  test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
      window.localStorage.setItem('spinneret.lang', 'en');
      window.localStorage.setItem('spinneret.theme', 'light');
    });
  });

  test('rejects wrong credentials and keeps the user on the login page', async ({ page }) => {
    await page.goto('/login');
    await page.getByLabel('Username').fill(ADMIN_USER);
    await page.getByLabel('Password').fill('definitely-not-the-password');
    await page.getByRole('button', { name: 'Sign in' }).click();

    await expect(page.getByRole('alert')).toHaveText(/Invalid username or password|Too many failed attempts/);
    await expect(page).toHaveURL(/\/login/);
  });

  test('signs in, keeps the requested page and signs out again', async ({ page }) => {
    // An unauthenticated deep link remembers where the user wanted to go.
    await page.goto('/proxies');
    await expect(page).toHaveURL(/\/login\?redirect=%2Fproxies/);

    await page.getByLabel('Username').fill(ADMIN_USER);
    await page.getByLabel('Password').fill(ADMIN_PASSWORD);
    await page.getByRole('button', { name: 'Sign in' }).click();

    await expect(page.getByRole('heading', { level: 1, name: 'Proxies' })).toBeVisible();
    await expect(page).toHaveURL(/\/proxies$/);

    await page.getByTestId('user-menu').click();
    await expect(page.getByRole('menu')).toContainText('Platform admin');
    await page.getByTestId('logout').click();

    await expect(page).toHaveURL(/\/login/);
    await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible();
    await dismissToasts(page);
  });
});

test.describe('shell', () => {
  test('switches the interface language and back', async ({ page }) => {
    await gotoPage(page, '/', 'Overview');
    await expect(page).toHaveTitle(/^Overview · Spinneret$/);

    await page.getByTestId('language-switcher').click();
    await page.getByTestId('language-zh-CN').click();

    await expect(page.getByRole('heading', { level: 1, name: '概览' })).toBeVisible();
    await expect(page).toHaveTitle(/^概览 · Spinneret$/);
    await expect(page.getByRole('navigation', { name: '主导航' })).toContainText('身份');

    await page.getByTestId('language-switcher').click();
    await page.getByTestId('language-en').click();
    await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible();
  });

  test('switches the theme between light and dark', async ({ page }) => {
    await gotoPage(page, '/', 'Overview');
    await expect(page.locator('html')).not.toHaveClass(/dark/);

    await page.getByTestId('theme-toggle').click();
    await page.getByRole('menuitemradio', { name: 'Dark' }).click();
    await expect(page.locator('html')).toHaveClass(/dark/);

    await page.getByTestId('theme-toggle').click();
    await page.getByRole('menuitemradio', { name: 'Light' }).click();
    await expect(page.locator('html')).not.toHaveClass(/dark/);
  });

  test('navigates through the sidebar to every console page', async ({ page }) => {
    await gotoPage(page, '/', 'Overview');
    const nav = page.getByRole('navigation', { name: 'Main navigation' });

    const pages: [string, string][] = [
      ['Identities', 'Identities'],
      ['Identity Types', 'Identity Types'],
      ['Accounts', 'Accounts'],
      ['Proxies', 'Proxies'],
      ['Sites', 'Sites'],
      ['Policies', 'Policies'],
      ['Heatmap', 'Cooldown heatmap'],
      ['Breakers', 'Breakers'],
      ['Config Center', 'Config Center'],
      ['Secrets', 'Secrets'],
      ['Requests', 'Request explorer'],
      ['Risk Events', 'Risk events'],
      ['Notifications', 'Notifications'],
      ['Tokens', 'API tokens'],
      ['Users', 'Users & roles'],
      ['Audit', 'Audit log'],
      ['Tenants', 'Tenants & namespaces'],
    ];

    for (const [link, heading] of pages) {
      await nav.getByRole('link', { name: link, exact: true }).click();
      await expect(page.getByRole('heading', { level: 1, name: heading })).toBeVisible();
    }
  });

  test('shows the profile page with the signed-in account', async ({ page }) => {
    await gotoPage(page, '/settings/profile', 'Profile');
    await expect(page.getByRole('definition').first()).toContainText(ADMIN_USER);
    await expect(page.getByText('Platform administrator')).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Change password' })).toBeVisible();
  });

  test('the About dialog names the maintainer and links the source and the manual', async ({ page }) => {
    await gotoPage(page, '/', 'Overview');
    await page.getByRole('button', { name: 'About' }).click();

    const about = dialog(page);
    await expect(about.getByRole('heading', { name: 'Spinneret' })).toBeVisible();
    // The server fills this in through GetMe; any build reports something.
    await expect(about.getByText('Server build')).toBeVisible();
    await expect(about.getByRole('link', { name: 'Apache-2.0' })).toBeVisible();
    await expect(about.getByRole('link', { name: 'TikHub' })).toBeVisible();

    const source = about.getByRole('link', { name: 'Source code' });
    await expect(source).toHaveAttribute('href', 'https://github.com/TikHub/Spinneret');
    await expect(source).toHaveAttribute('target', '_blank');
    await expect(about.getByRole('link', { name: 'Documentation' })).toHaveAttribute(
      'href',
      'https://github.com/TikHub/Spinneret/tree/main/documents/en',
    );

    await page.keyboard.press('Escape');
    await expect(about).toBeHidden();
  });

  test('renders a not-found page for an unknown route', async ({ page }) => {
    await page.goto('/this-route-does-not-exist');
    await expect(page.getByRole('heading', { name: 'Page not found' })).toBeVisible();
    await page.getByRole('link', { name: 'Go to overview' }).click();
    await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible();
  });
});
