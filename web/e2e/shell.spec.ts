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

  test('the About dialog states the copyright, the licence, the maintainer and the source', async ({
    page,
  }) => {
    await gotoPage(page, '/', 'Overview');
    await page.getByRole('button', { name: 'About' }).click();

    const about = dialog(page);
    await expect(about.getByRole('heading', { name: 'Spinneret' })).toBeVisible();
    await expect(about.getByText(/^© \d{4} TikHub$/)).toBeVisible();
    await expect(about.getByRole('link', { name: 'Apache-2.0' })).toHaveAttribute(
      'href',
      'https://github.com/TikHub/Spinneret/blob/main/LICENSE',
    );
    // Exact: the source link's text contains the organisation name as well.
    await expect(about.getByRole('link', { name: 'TikHub', exact: true })).toHaveAttribute(
      'href',
      'https://github.com/TikHub',
    );

    const source = about.getByRole('link', { name: 'github.com/TikHub/Spinneret' });
    await expect(source).toHaveAttribute('href', 'https://github.com/TikHub/Spinneret');
    await expect(source).toHaveAttribute('target', '_blank');

    await page.keyboard.press('Escape');
    await expect(about).toBeHidden();
  });

  test('Settings → System shows the running build and links the manual and the source', async ({
    page,
    api,
  }) => {
    // The page shows the build GetMe reports; every build reports one ("dev" when
    // built from source without a release number).
    const me = await api.call<{ server_version?: string }>('AuthService/GetMe');
    const build = me.server_version ?? '';
    expect(build, 'GetMe reports the server build').not.toBe('');

    // Reached from the account menu, its only entry point.
    await gotoPage(page, '/', 'Overview');
    await page.getByTestId('user-menu').click();
    await page.getByRole('menuitem', { name: 'System' }).click();
    await expect(page.getByRole('heading', { level: 1, name: 'System' })).toBeVisible();
    await expect(page).toHaveURL(/\/settings\/system$/);

    const main = page.getByRole('main');
    await expect(main.getByRole('heading', { name: 'Version and updates' })).toBeVisible();
    await expect(main.getByText(build, { exact: true })).toBeVisible();
    // Offered but not pressed: the check reaches the public release feed, which a
    // test run has no business doing.
    await expect(main.getByRole('button', { name: 'Check for updates' })).toBeEnabled();

    await expect(main.getByRole('link', { name: /^Documentation/ })).toHaveAttribute(
      'href',
      'https://github.com/TikHub/Spinneret/tree/main/documents/en',
    );
    const source = main.getByRole('link', { name: /^Source code/ });
    await expect(source).toHaveAttribute('href', 'https://github.com/TikHub/Spinneret');
    await expect(source).toHaveAttribute('target', '_blank');
  });

  test('renders a not-found page for an unknown route', async ({ page }) => {
    await page.goto('/this-route-does-not-exist');
    await expect(page.getByRole('heading', { name: 'Page not found' })).toBeVisible();
    await page.getByRole('link', { name: 'Go to overview' }).click();
    await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible();
  });
});
