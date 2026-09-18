import { NAMESPACE, uniqueName } from './fixtures/env';
import { expect, test } from './fixtures/test';
import { dialog, expectToast, fillEditor, gotoPage, selectOption } from './fixtures/ui';

test.describe('config center', () => {
  test('creates a JSON item, publishes it, publishes a second version and rolls back', async ({
    page,
    api,
  }) => {
    const group = uniqueName('cfg');
    const key = 'settings.json';
    const label = `${group}/${key}`;

    try {
      await gotoPage(page, '/config', 'Config Center');

      // --- create a draft ------------------------------------------------------
      await page.getByRole('button', { name: 'New item' }).click();
      const create = dialog(page);
      await expect(create.getByRole('heading', { name: 'New config item' })).toBeVisible();
      await create.getByLabel(/^Group/).fill(group);
      await create.getByLabel(/^Key/).fill(key);
      await selectOption(create, /^Format/, 'JSON');
      await create.getByLabel('Description').fill('Created by the console e2e suite');
      await fillEditor(page, 'Content', '{"rps": 5}');
      const publishNow = create.getByRole('switch', { name: 'Publish as version 1' });
      if (await publishNow.isChecked()) await publishNow.click();
      await create.getByRole('button', { name: 'Create draft' }).click();
      await expectToast(page, 'Config item created as a draft');

      await expect(page.getByRole('heading', { level: 2, name: new RegExp(key) })).toBeVisible();
      await expect(page.getByText('Never published')).toBeVisible();

      // --- publish version 1 -----------------------------------------------------
      await page.getByRole('button', { name: 'Publish', exact: true }).click();
      const publish = page.getByRole('alertdialog');
      await expect(publish).toContainText(`Publish ${label}`);
      await publish.getByLabel('Comment').fill('first version');
      await publish.getByRole('button', { name: 'Publish v1' }).click();
      await expectToast(page, new RegExp(`Published ${label} as v1`));

      // --- edit and publish version 2 ----------------------------------------------
      await fillEditor(page, 'Content', '{"rps": 9}');
      await expect(page.getByText('Unsaved changes')).toBeVisible();
      await page.getByRole('button', { name: 'Save draft' }).click();
      await expectToast(page, 'Draft saved');
      await page.getByRole('button', { name: 'Publish', exact: true }).click();
      const publish2 = page.getByRole('alertdialog');
      await publish2.getByLabel('Comment').fill('raise the rate');
      await publish2.getByRole('button', { name: 'Publish v2' }).click();
      await expectToast(page, new RegExp(`Published ${label} as v2`));

      // --- versions and diff ---------------------------------------------------------
      await page.getByRole('tab', { name: 'Versions' }).click();
      await expect(page.getByRole('row', { name: /first version/ })).toBeVisible();
      await expect(page.getByRole('row', { name: /raise the rate/ })).toBeVisible();
      await page
        .getByRole('row', { name: /first version/ })
        .getByRole('button', { name: 'Compare with current' })
        .click();
      await expect(page.getByText('"rps": 9').first()).toBeVisible();

      // --- roll back to version 1 -------------------------------------------------------
      await page.getByRole('button', { name: 'Roll back', exact: true }).click();
      const rollback = page.getByRole('alertdialog');
      await expect(rollback).toContainText(`Roll back ${label}`);
      await selectOption(rollback, 'Version', /v1/);
      await rollback.getByRole('button', { name: 'Roll back to v1' }).click();
      await expectToast(page, new RegExp(`Rolled back ${label} to v1`));
      await expect(page.getByRole('row', { name: /Rollback of v1/ })).toBeVisible();
    } finally {
      const items = await api.tryCall<{ items?: { id: string; group: string; key: string }[] }>(
        'ConfigAdminService/ListConfigItems',
        { namespace: NAMESPACE, page_size: 500 },
      );
      const item = items?.items?.find((i) => i.group === group && i.key === key);
      if (item) await api.tryCall('ConfigAdminService/DeleteConfigItem', { id: item.id });
    }
  });
});
