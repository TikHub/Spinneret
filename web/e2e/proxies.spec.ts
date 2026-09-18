import { NAMESPACE, uniqueName } from './fixtures/env';
import { expect, test } from './fixtures/test';
import { dialog, expectToast, gotoPage, selectOption } from './fixtures/ui';

test.describe('proxies', () => {
  test('imports proxies from URL lines, checks one and disables and enables them in bulk', async ({
    page,
    api,
  }) => {
    // Loopback ports that refuse instantly inside the server container: the health
    // check fails fast instead of waiting for a network timeout.
    const tag = uniqueName('proxy');
    const urls = [
      `http://e2e:secret@127.0.0.1:9001 kind=datacenter region=EU provider=${tag} tags=${tag}`,
      `http://e2e:secret@127.0.0.1:9002 kind=residential region=US provider=${tag} tags=${tag}`,
    ];
    const ids: string[] = [];

    try {
      await gotoPage(page, '/proxies', 'Proxies');

      // --- import: dry run then import ---------------------------------------
      await page.getByRole('button', { name: 'Import' }).click();
      const importDialog = dialog(page);
      await expect(importDialog.getByRole('heading', { name: 'Import proxies' })).toBeVisible();
      await selectOption(importDialog, 'Format', 'URL lines');
      await importDialog.getByLabel('Data').fill(urls.join('\n'));
      await expect(importDialog.getByText('2 data lines')).toBeVisible();

      await importDialog.getByRole('button', { name: 'Dry run' }).click();
      await expect(importDialog.getByText('Dry run result (nothing was stored)')).toBeVisible();
      await expectToast(page, /Dry run: 2 new/);

      await importDialog.getByRole('button', { name: 'Import', exact: true }).click();
      await expectToast(page, /Imported: 2 created/);
      await importDialog.getByRole('button', { name: 'Close' }).first().click();

      // --- find them again through the search filter ----------------------------
      await page.getByRole('textbox', { name: 'Search URL, host, exit IP or ID…' }).fill('127.0.0.1:900');
      const rows = page.getByRole('row').filter({ hasText: '127.0.0.1:900' });
      // The whole table must be down to the two imported proxies before anything
      // selects "all rows".
      await expect(page.getByRole('row')).toHaveCount(3);
      await expect(rows).toHaveCount(2);
      await expect(rows.filter({ hasText: '9001' })).toContainText('Datacenter');
      await expect(rows.filter({ hasText: '9002' })).toContainText('Residential');

      const first = rows.filter({ hasText: '9001' });
      const proxyName = 'http://127.0.0.1:9001';

      // --- check now --------------------------------------------------------------
      await first.getByRole('button', { name: `Check ${proxyName} now` }).click();
      await expectToast(
        page,
        new RegExp(`Check (passed|failed): ${proxyName.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}`),
      );

      // --- bulk disable and enable --------------------------------------------------
      await page.getByRole('checkbox', { name: 'Select all rows' }).click();
      await expect(page.getByText('2 selected')).toBeVisible();

      await page.getByRole('button', { name: 'Disable', exact: true }).click();
      await page.getByRole('alertdialog').getByRole('button', { name: 'Disable', exact: true }).click();
      await expectToast(page, /Disable: 2 proxies updated/);
      await expect(page.getByRole('cell', { name: 'Disabled' })).toHaveCount(2);

      await page.getByRole('checkbox', { name: 'Select all rows' }).click();
      await page.getByRole('button', { name: 'Enable', exact: true }).click();
      await page.getByRole('alertdialog').getByRole('button', { name: 'Enable', exact: true }).click();
      await expectToast(page, /Enable: 2 proxies updated/);
      await expect(page.getByRole('cell', { name: 'Disabled' })).toHaveCount(0);

      // --- the providers tab aggregates them -------------------------------------------
      await page.getByRole('tab', { name: 'Providers' }).click();
      await expect(page.getByRole('cell', { name: tag })).toBeVisible();
      await page.getByRole('tab', { name: 'Proxies' }).click();

      // --- delete them again (cleanup through the UI) -------------------------------------
      await page.getByRole('textbox', { name: 'Search URL, host, exit IP or ID…' }).fill('127.0.0.1:900');
      await expect(page.getByRole('row')).toHaveCount(3);
      await page.getByRole('checkbox', { name: 'Select all rows' }).click();
      await page.getByRole('button', { name: 'Delete' }).click();
      const deleteDialog = page.getByRole('alertdialog');
      await expect(deleteDialog).toContainText('Delete 2 proxies');
      await deleteDialog.getByLabel('Type delete to confirm').fill('delete');
      await deleteDialog.getByRole('button', { name: 'Delete' }).click();
      await expectToast(page, /2 proxies deleted/);
      await expect(rows).toHaveCount(0);
    } finally {
      // Safety net if the UI part failed before the delete step.
      const list = await api.tryCall<{ proxies?: { id: string; url: string }[] }>(
        'ProxyAdminService/ListProxies',
        { namespace: NAMESPACE, page_size: 200, search: '127.0.0.1:900' },
      );
      for (const proxy of list?.proxies ?? []) ids.push(proxy.id);
      if (ids.length > 0) await api.tryCall('ProxyAdminService/DeleteProxies', { ids });
    }
  });
});
