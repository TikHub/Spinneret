import { NAMESPACE, uniqueName, SEEDED_SITE } from './fixtures/env';
import { expect, test } from './fixtures/test';
import { dialog, editorText, expectToast, gotoPage, replaceInEditor, selectOption } from './fixtures/ui';

test.describe('policies', () => {
  test('creates a rotation policy in the form, publishes it, edits the YAML, compares versions and binds it', async ({
    page,
    api,
  }) => {
    const policy = uniqueName('rot');
    const site = uniqueName('polsite');
    await api.createSite(site, { displayName: 'Policy drill' });

    try {
      await gotoPage(page, '/policies', 'Policies');

      // --- create a draft ----------------------------------------------------
      await page.getByRole('button', { name: 'New policy' }).click();
      const create = dialog(page);
      await create.getByRole('radio', { name: /^Rotation/ }).click();
      await create.getByLabel(/^Name/).fill(policy);
      // Keep it a draft so the publish step is explicit.
      const publishSwitch = create.getByRole('switch', { name: 'Publish as version 1' });
      if (await publishSwitch.isChecked()) await publishSwitch.click();
      await create.getByRole('button', { name: 'Create draft' }).click();
      await expectToast(page, 'Policy created as a draft');

      await expect(page.getByRole('heading', { level: 2, name: policy })).toBeVisible();

      // --- edit in form mode --------------------------------------------------
      await page.getByRole('group', { name: 'Editor mode' }).getByRole('button', { name: 'Form' }).click();
      await page.getByLabel('Max concurrent leases').fill('2');
      await page.getByRole('textbox', { name: 'Reuse interval' }).fill('45s');
      await expect(page.getByText('Unsaved changes')).toBeVisible();
      await page.getByRole('button', { name: 'Save draft' }).click();
      await expectToast(page, 'Draft saved');

      // The form regenerates the YAML.
      await page.getByRole('group', { name: 'Editor mode' }).getByRole('button', { name: 'YAML' }).click();
      await expect.poll(() => editorText(page, `YAML of policy ${policy}`)).toContain('reuse_interval: 45s');

      // --- publish version 1 ---------------------------------------------------
      await page.getByRole('button', { name: 'Publish', exact: true }).click();
      const publish = page.getByRole('alertdialog');
      await expect(publish).toContainText(`Publish ${policy}?`);
      await publish.getByLabel('Comment').fill('initial rotation policy');
      await publish.getByRole('button', { name: 'Publish', exact: true }).click();
      await expectToast(page, 'Published version 1');

      // --- edit the YAML draft and publish version 2 -------------------------------
      await replaceInEditor(page, `YAML of policy ${policy}`, 'reuse_interval', '45s', '90s');
      await expect(page.getByText('Unsaved changes')).toBeVisible();
      await page.getByRole('button', { name: 'Validate' }).click();
      await page.getByRole('button', { name: 'Publish', exact: true }).click();
      const publish2 = page.getByRole('alertdialog');
      await publish2.getByLabel('Comment').fill('longer reuse interval');
      await publish2.getByRole('button', { name: 'Publish', exact: true }).click();
      await expectToast(page, 'Published version 2');

      // --- versions and diff --------------------------------------------------------
      const detailTabs = page.getByRole('tablist').last();
      await detailTabs.getByRole('tab', { name: /^Versions/ }).click();
      await expect(page.getByRole('row', { name: /^v?1 initial rotation policy/ })).toBeVisible();
      await page.getByRole('button', { name: 'Compare v1' }).click();
      // The server normalises 90s to its canonical duration form.
      await expect(page.getByText('1m30s').first()).toBeVisible();

      // --- bind it to the test site -------------------------------------------------
      await detailTabs.getByRole('tab', { name: /^Bindings/ }).click();
      await page.getByRole('button', { name: 'Bind', exact: true }).click();
      const bind = dialog(page);
      await expect(bind).toContainText(`Bind ${policy}`);
      await selectOption(bind, /^Site/, new RegExp(site));
      await bind.getByRole('button', { name: 'Bind', exact: true }).click();
      await expectToast(page, 'Binding saved');
      await expect(page.getByRole('row', { name: new RegExp(site) })).toBeVisible();

      // --- remove the binding, then delete the policy -------------------------------
      await page.getByRole('button', { name: 'Remove binding' }).click();
      const unbind = page.getByRole('alertdialog');
      await expect(unbind).toContainText('Remove binding?');
      await unbind.getByRole('button', { name: 'Remove binding' }).click();
      await expectToast(page, 'Binding removed');

      await detailTabs.getByRole('tab', { name: /^Editor/ }).click();
      await page.getByRole('button', { name: 'Delete', exact: true }).click();
      const remove = page.getByRole('alertdialog');
      await remove.getByLabel(`Type ${policy} to confirm`).fill(policy);
      await remove.getByRole('button', { name: 'Delete policy' }).click();
      await expectToast(page, 'Policy deleted');
      await expect(page.getByRole('navigation', { name: 'Policies by kind' })).not.toContainText(policy);
    } finally {
      await api.deleteSite(site);
      const list = await api.tryCall<{ policies?: { id: string; name: string }[] }>(
        'PolicyAdminService/ListPolicies',
        { namespace: NAMESPACE, page_size: 500 },
      );
      const leftover = list?.policies?.find((p) => p.name === policy);
      if (leftover) await api.tryCall('PolicyAdminService/DeletePolicy', { id: leftover.id });
    }
  });

  test('the rule debugger turns a 429 report into a cooldown action', async ({ page }) => {
    await gotoPage(page, '/policies', 'Policies');
    await page.getByRole('tab', { name: 'Rule debugger' }).click();

    await selectOption(page, /^Site/, new RegExp(`\\(${SEEDED_SITE}\\)$`));
    await selectOption(page, /^Client/, 'web');
    await page.getByRole('combobox', { name: /^Endpoint group/ }).click();
    await page.getByRole('option').first().click();

    await page.getByLabel('HTTP status').fill('429');
    await page.getByRole('button', { name: 'Run debugger' }).click();

    const result = page.locator('main');
    await expect(result.getByText('Outcome', { exact: true })).toBeVisible();
    await expect(result.getByText('rate_limited').first()).toBeVisible();
    await expect(result.getByRole('cell', { name: /Cooldown/i }).first()).toBeVisible();
  });
});
