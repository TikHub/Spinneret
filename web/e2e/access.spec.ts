import { NAMESPACE, uniqueName } from './fixtures/env';
import { expect, test } from './fixtures/test';
import { dialog, expectToast, gotoPage, selectOption } from './fixtures/ui';

test.describe('access', () => {
  test('creates an API token, shows its plaintext once and revokes it', async ({ page, api }) => {
    const name = uniqueName('token');

    try {
      await gotoPage(page, '/access/tokens', 'API tokens');

      await page.getByRole('button', { name: 'Create token' }).click();
      const form = dialog(page);
      await expect(form.getByRole('heading', { name: 'Create API token' })).toBeVisible();
      await form.getByLabel(/^Name/).fill(name);
      await form.getByLabel('Description').fill('Console e2e token');
      await form.getByRole('button', { name: 'Crawler node' }).click();
      await expect(form.getByRole('combobox', { name: 'Scope 1', exact: true })).toHaveText('lease:acquire');
      await form.getByRole('button', { name: 'Create token' }).click();

      // The plaintext is shown exactly once, behind an explicit acknowledgement.
      const secret = dialog(page);
      await expect(secret.getByRole('heading', { name: `Token ${name} created` })).toBeVisible();
      await expect(secret).toContainText('This is the only time the token is shown');
      const plaintext = (await secret.getByRole('group', { name: 'Token' }).innerText()).trim();
      expect(plaintext).toMatch(/^spn_/);
      await secret.getByRole('button', { name: 'Copy token' }).click();
      await expect(secret.getByRole('button', { name: 'Done' })).toBeDisabled();
      await secret.getByRole('checkbox', { name: /I have saved the token/ }).check();
      await secret.getByRole('button', { name: 'Done' }).click();
      await expectToast(page, `Token ${name} created`);

      const row = page.getByRole('row', { name: new RegExp(name) });
      await expect(row).toContainText('Active');
      await expect(row).toContainText('lease:acquire');
      // The plaintext is never shown again.
      await expect(page.getByText(plaintext)).toHaveCount(0);

      // --- revoke ----------------------------------------------------------------
      await row.getByRole('button', { name: 'Revoke' }).click();
      const confirm = page.getByRole('alertdialog');
      await expect(confirm).toContainText(`Revoke token ${name}?`);
      await confirm.getByLabel(`Type ${name} to confirm`).fill(name);
      await confirm.getByRole('button', { name: 'Revoke' }).click();
      await expectToast(page, `Token ${name} revoked`);

      await page.getByRole('switch', { name: 'Show revoked' }).click();
      await expect(page.getByRole('row', { name: new RegExp(name) })).toContainText('Revoked');
    } finally {
      const list = await api.tryCall<{ tokens?: { id: string; name: string; revoked_at?: string }[] }>(
        'AccessAdminService/ListTokens',
        { namespace: NAMESPACE, page_size: 500, include_revoked: true },
      );
      const token = list?.tokens?.find((t) => t.name === name && !t.revoked_at);
      if (token) await api.tryCall('AccessAdminService/RevokeToken', { id: token.id });
    }
  });

  test('creates an operator restricted to one site and that user only sees that site', async ({
    page,
    api,
    browser,
  }) => {
    const username = uniqueName('op').replace(/-/g, '.');
    const password = 'Console-e2e-9f3a!';
    const site = uniqueName('opsite');
    const otherSite = uniqueName('othersite');
    await api.createSite(site, { displayName: 'Operator site' });
    await api.createSite(otherSite, { displayName: 'Invisible site' });

    try {
      await gotoPage(page, '/access/users', 'Users & roles');

      await page.getByRole('button', { name: 'Create user' }).click();
      const form = dialog(page);
      await form.getByLabel(/^Username/).fill(username);
      await form.getByLabel('Display name').fill('Console e2e operator');
      await form.getByLabel(/^Initial password/).fill(password);
      await selectOption(form, 'Role', 'Operator');
      await selectOption(form, 'Namespace', NAMESPACE);
      // Restrict the binding to a single site.
      await form.getByLabel(/^Sites/).click();
      const sitePicker = page.getByRole('group', { name: 'Sites' });
      await sitePicker.getByRole('checkbox', { name: site }).check();
      await page.keyboard.press('Escape');
      await form.getByRole('button', { name: 'Create user' }).click();
      await expectToast(page, `User ${username} created`);

      const row = page.getByRole('row', { name: new RegExp(username) });
      await expect(row).toContainText('Operator');
      await expect(row).toContainText('Active');

      // --- sign in as that user in a separate context --------------------------
      // A context without the administrator session of the suite.
      const context = await browser.newContext({ storageState: { cookies: [], origins: [] } });
      const restricted = await context.newPage();
      try {
        await restricted.addInitScript(() => {
          window.localStorage.setItem('spinneret.lang', 'en');
          window.localStorage.setItem('spinneret.theme', 'light');
        });
        await restricted.goto('/login');
        await restricted.getByLabel('Username').fill(username);
        await restricted.getByLabel('Password').fill(password);
        await restricted.getByRole('button', { name: 'Sign in' }).click();
        await expect(restricted.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible();

        // Only the site of the binding is visible.
        await restricted.goto('/sites');
        const list = restricted.getByRole('navigation', { name: 'Sites' });
        await expect(list.getByRole('listitem')).toHaveCount(1);
        await expect(list).toContainText(site);
        await expect(list).not.toContainText(otherSite);

        // Namespace-level administration is not part of the operator role.
        await restricted.goto('/access/tokens');
        await expect(restricted.getByText('Permission denied')).toBeVisible();
        await expect(
          restricted.getByRole('navigation', { name: 'Main navigation' }).getByRole('link', {
            name: 'Users',
          }),
        ).toHaveCount(0);
      } finally {
        await context.close();
      }
    } finally {
      // Users cannot be deleted, so the account is disabled instead. ListUsers
      // returns access wrappers, with the account under `user`.
      const users = await api.tryCall<{ users?: { user: { id: string; username: string } }[] }>(
        'AccessAdminService/ListUsers',
        { tenant_id: api.tenantId, page_size: 200, all_users: true },
      );
      const user = users?.users?.find((u) => u.user.username === username)?.user;
      if (user) await api.call('AccessAdminService/UpdateUser', { id: user.id, disabled: true });
      await api.deleteSite(site);
      await api.deleteSite(otherSite);
    }
  });

  test('the audit log lists console actions and filters by action', async ({ page, api }) => {
    // Produce one auditable action right before reading the log.
    const site = uniqueName('audit');
    await api.createSite(site);
    await api.deleteSite(site);

    await gotoPage(page, '/access/audit', 'Audit log');
    await expect(page.getByRole('row').nth(1)).toBeVisible();

    await page.getByRole('textbox', { name: 'Action, e.g. secret.read' }).fill('site.create');
    await expect(page.getByRole('cell', { name: 'site.create' }).first()).toBeVisible();
    await expect(page.getByRole('row', { name: new RegExp(site) }).first()).toBeVisible();

    // Row details show the recorded request payload.
    await page.getByRole('button', { name: 'Show details' }).first().click();
    await expect(page.getByText('Entry ID').first()).toBeVisible();
    await expect(page.getByRole('button', { name: 'Hide details' }).first()).toBeVisible();
  });
});
