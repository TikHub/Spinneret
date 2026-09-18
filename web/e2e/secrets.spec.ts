import { NAMESPACE, uniqueName } from './fixtures/env';
import { expect, test } from './fixtures/test';
import { dialog, expectToast, gotoPage } from './fixtures/ui';

test.describe('secrets', () => {
  test('creates a secret, reveals it after a confirmation and stores a second version', async ({
    page,
    api,
  }) => {
    const path = `e2e/${uniqueName('secret')}`;
    const value = 'first-value-01234';
    const nextValue = 'second-value-56789';

    try {
      await gotoPage(page, '/secrets', 'Secrets');

      // --- create -------------------------------------------------------------
      await page.getByRole('button', { name: 'New secret' }).first().click();
      const form = dialog(page);
      await expect(form.getByRole('heading', { name: 'New secret' })).toBeVisible();
      await form.getByLabel(/^Path/).fill(path);
      await form.getByLabel(/^Value/).fill(value);
      await form.getByLabel('Description').fill('Console e2e secret');
      await form.getByRole('button', { name: 'Create' }).click();
      await expectToast(page, `Secret ${path} created`);

      const row = page.getByRole('row', { name: new RegExp(path) });
      await expect(row).toBeVisible();
      await expect(row).toContainText('v1');
      // The value never appears in the list.
      await expect(page.getByText(value)).toHaveCount(0);

      // --- reveal (audited, needs an explicit confirmation) ---------------------
      await row.getByRole('button', { name: 'Reveal' }).click();
      const reveal = page.getByRole('alertdialog');
      await expect(reveal).toContainText(`Reveal ${path}?`);
      await expect(reveal).toContainText('recorded in the audit log');
      await reveal.getByLabel('Type reveal to confirm').fill('reveal');
      await reveal.getByRole('button', { name: 'Reveal value' }).click();
      await expect(page.getByText(value).first()).toBeVisible();
      await page.getByRole('button', { name: 'Close' }).first().click();

      // --- new version -----------------------------------------------------------
      await page.getByRole('cell', { name: path }).click();
      const detail = page.getByRole('dialog').last();
      await detail.getByRole('button', { name: 'Edit' }).click();
      const edit = dialog(page);
      await edit.getByLabel(/^New value/).fill(nextValue);
      await edit.getByRole('button', { name: 'Save' }).click();
      await expectToast(page, new RegExp(`Secret ${path} updated`));

      // --- versions tab --------------------------------------------------------------
      await page.getByRole('tab', { name: 'Versions' }).click();
      await expect(page.getByRole('row', { name: /2/ }).first()).toBeVisible();
      await page.getByRole('tab', { name: 'Access logs' }).click();
      await expect(page.getByRole('cell', { name: 'OK' }).first()).toBeVisible();
    } finally {
      const list = await api.tryCall<{ secrets?: { id: string; path: string }[] }>(
        'SecretAdminService/ListSecrets',
        { namespace: NAMESPACE, page_size: 500 },
      );
      const secret = list?.secrets?.find((s) => s.path === path);
      if (secret) await api.tryCall('SecretAdminService/DeleteSecret', { id: secret.id });
    }
  });
});
