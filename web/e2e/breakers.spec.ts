import { uniqueName } from './fixtures/env';
import { expect, test } from './fixtures/test';
import { expectToast, gotoPage, selectOption } from './fixtures/ui';

test.describe('breakers', () => {
  test('opens a breaker manually and closes it again', async ({ page, api }) => {
    const site = uniqueName('brk');
    await api.createSite(site, { displayName: 'Breaker drill' });
    await api.createEndpointGroup(site, 'search');

    try {
      await gotoPage(page, '/breakers', 'Breakers');
      await selectOption(page, /^Site/, new RegExp(site));

      // The site filter is applied asynchronously, so the row is matched by its
      // site as well: other sites have a "search" group too.
      const row = page.getByRole('row', { name: new RegExp(`^search ${site} `) });
      await expect(row).toContainText('Closed');

      // --- open manually ---------------------------------------------------
      await row.getByRole('button', { name: 'Open' }).click();
      const open = page.getByRole('alertdialog');
      await expect(open).toContainText('Open breaker?');
      await open.getByLabel('Duration').fill('10m');
      await open.getByLabel('Reason').fill('console e2e drill');
      await open.getByRole('button', { name: 'Open breaker' }).click();
      await expectToast(page, 'Breaker of search opened');

      await expect(row).toContainText('Open');
      await expect(row).toContainText('Manual');
      await expect(row).toContainText('console e2e drill');

      // --- close again -------------------------------------------------------
      await row.getByRole('button', { name: 'Close' }).click();
      const close = page.getByRole('alertdialog');
      await expect(close).toContainText('Close breaker?');
      await close.getByRole('button', { name: 'Close breaker' }).click();
      await expectToast(page, 'Breaker of search closed');
      await expect(row).toContainText('Closed');

      // --- the history tab records both transitions -----------------------------
      await page.getByRole('tab', { name: 'History' }).click();
      await selectOption(page, /^Site/, new RegExp(site));
      await expect(page.getByRole('row', { name: /Manual/ }).first()).toBeVisible();
      await expect(page.getByText('console e2e drill').first()).toBeVisible();
    } finally {
      await api.deleteSite(site);
    }
  });

  test('pauses and resumes a site from the site switches tab', async ({ page, api }) => {
    const site = uniqueName('switch');
    await api.createSite(site, { displayName: 'Switch drill' });

    try {
      await gotoPage(page, '/breakers', 'Breakers');
      await page.getByRole('tab', { name: 'Site switches' }).click();

      const row = page.getByRole('row', { name: new RegExp(site) });
      const pause = row.getByRole('switch', { name: `Pause site ${site}` });
      await expect(pause).toBeChecked();
      await pause.click();

      const dialog = page.getByRole('alertdialog');
      await expect(dialog).toContainText(`Pause site ${site}?`);
      await dialog.getByLabel('Reason').fill('console e2e switch drill');
      await dialog.getByRole('button', { name: 'Pause site' }).click();
      await expectToast(page, `Site ${site} paused`);
      await expect(row).toContainText('Paused');
      await expect(row).toContainText('console e2e switch drill');

      const resume = row.getByRole('switch', { name: `Resume site ${site}` });
      await expect(resume).not.toBeChecked();
      await resume.click();
      await page.getByRole('alertdialog').getByRole('button', { name: 'Resume site' }).click();
      await expectToast(page, `Site ${site} resumed`);
      await expect(row.getByRole('switch', { name: `Pause site ${site}` })).toBeChecked();
    } finally {
      await api.deleteSite(site);
    }
  });
});
