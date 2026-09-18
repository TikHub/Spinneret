import { uniqueName } from './fixtures/env';
import { expect, test } from './fixtures/test';
import { dialog, editorText, expectToast, fillEditor, gotoPage, selectOption } from './fixtures/ui';

/** Sample payload rendered by the delivery preview. */
const SAMPLE_PAYLOAD = JSON.stringify(
  {
    cookies: { sessionid: 'e2e-session', csrf_token: 'e2e-csrf' },
    user_agent: 'Mozilla/5.0 (console e2e)',
    signature: 'e2e-signature',
  },
  null,
  2,
);

test.describe('identity types', () => {
  test('creates a type from the YAML example and renders its delivery preview', async ({ page, api }) => {
    const site = uniqueName('types');
    await api.createSite(site, { displayName: 'Identity type drill' });

    try {
      await gotoPage(page, '/identity-types', 'Identity Types');
      await page.getByRole('button', { name: 'New type' }).click();

      const editor = dialog(page);
      await expect(editor.getByRole('heading', { name: 'New identity type' })).toBeVisible();
      await selectOption(editor, /^Site/, new RegExp(site));

      // The editor starts from a stub, so inserting an example asks first.
      await editor.getByRole('button', { name: 'Insert example' }).click();
      await page.getByRole('menuitem', { name: 'Web cookie' }).click();
      const replace = page.getByRole('alertdialog');
      await expect(replace).toContainText('Replace the current YAML?');
      await replace.getByRole('button', { name: 'Replace' }).click();

      await expect.poll(() => editorText(page, 'Identity type YAML')).toContain('name: web_cookie');

      // --- delivery preview ------------------------------------------------
      await editor.getByRole('tab', { name: 'Delivery preview' }).click();
      await fillEditor(page, 'Sample payload', SAMPLE_PAYLOAD);
      await editor.getByRole('button', { name: 'Render' }).click();

      await expect(editor.getByText('Cookies for the HTTP client')).toBeVisible();
      await expect(editor.getByText('Cookie request header')).toBeVisible();
      await expect(editor.getByRole('cell', { name: 'Mozilla/5.0 (console e2e)' })).toBeVisible();

      // --- create ------------------------------------------------------------
      await editor.getByRole('tab', { name: 'Spec (YAML)' }).click();
      await editor.getByRole('button', { name: 'Create' }).click();
      await expectToast(page, 'Identity type web_cookie created');

      // The type name comes from the shared example, so the row is located by
      // this spec's unique site instead.
      const row = page.getByRole('row', { name: new RegExp(site) });
      await expect(row).toBeVisible();
      await expect(row).toContainText('web_cookie');
      await expect(row).toContainText(`${site} · web`);
      await expect(row).toContainText('3 fields');
      await expect(row).toContainText('cookies.sessionid');
      await expect(row).toContainText('Probe');

      // --- filter -------------------------------------------------------------
      const search = page.getByRole('textbox', { name: 'Search type names…' });
      await search.fill('web_cookie');
      await expect(row).toBeVisible();
      await search.fill(`${site}-no-such-type`);
      await expect(row).toHaveCount(0);
      await search.fill('');
      await expect(row).toBeVisible();

      // --- delivery preview from the row menu ---------------------------------
      await row.getByRole('button', { name: 'Actions' }).click();
      await page.getByRole('menuitem', { name: 'Delivery preview' }).click();
      await expect(dialog(page).getByRole('tab', { name: 'Delivery preview' })).toHaveAttribute(
        'aria-selected',
        'true',
      );
      await expect(dialog(page).getByText('Render the sample payload')).toBeVisible();
      await dialog(page).getByRole('button', { name: 'Close' }).first().click();

      // --- delete --------------------------------------------------------------
      await row.getByRole('button', { name: 'Actions' }).click();
      await page.getByRole('menuitem', { name: 'Delete' }).click();
      const confirm = page.getByRole('alertdialog');
      await expect(confirm).toContainText('Delete identity type web_cookie?');
      await confirm.getByLabel('Type web_cookie to confirm').fill('web_cookie');
      await confirm.getByRole('button', { name: 'Delete' }).click();
      await expectToast(page, 'Identity type web_cookie deleted');
      await expect(row).toHaveCount(0);
    } finally {
      await api.deleteSite(site);
    }
  });
});
