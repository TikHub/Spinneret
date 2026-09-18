import { uniqueName } from './fixtures/env';
import { expect, test } from './fixtures/test';
import { dialog, expectToast, gotoPage, selectOption } from './fixtures/ui';

test.describe('sites', () => {
  test('creates a site, adds an endpoint group, edits URI rules and resolves them with the tester', async ({
    page,
    api,
  }) => {
    const site = uniqueName('site');
    const group = 'detail';

    try {
      await gotoPage(page, '/sites', 'Sites');

      // --- create the site through the UI ---------------------------------
      await page.getByRole('button', { name: 'New site' }).click();
      const form = dialog(page);
      await expect(form.getByRole('heading', { name: 'New site' })).toBeVisible();
      await form.getByLabel(/^Name\b/).fill(site);
      await form.getByLabel('Display name').fill('End-to-end site');
      await form.getByLabel('Description').fill('Created by the console e2e suite.');
      const clients = form.getByLabel(/^Clients\b/);
      await clients.fill('web');
      await clients.press('Enter');
      await clients.fill('app');
      await clients.press('Enter');
      await form.getByRole('button', { name: 'Create' }).click();

      await expectToast(page, `Site ${site} created`);
      await expect(page.getByRole('heading', { level: 2, name: 'End-to-end site' })).toBeVisible();
      await expect(page.getByRole('tab', { name: 'web' })).toBeVisible();
      await expect(page.getByRole('tab', { name: 'app' })).toBeVisible();

      // Every client starts with its own _default group.
      const groupsRegion = page.getByRole('region', { name: 'Endpoint groups' });
      await expect(groupsRegion.getByRole('row', { name: /_default/ })).toBeVisible();

      // --- add an endpoint group ------------------------------------------
      await page.getByRole('button', { name: 'New endpoint group' }).click();
      const groupForm = dialog(page);
      await groupForm.getByLabel(/^Name\b/).fill(group);
      await groupForm.getByLabel('Description').fill('Item detail pages');
      await groupForm.getByLabel(/^Low watermark/).fill('5');
      await groupForm.getByRole('button', { name: 'Create' }).click();

      await expectToast(page, `Endpoint group ${group} created`);

      // --- URI rules (the rule editor opens right after creating a group) ---
      const sheet = page.getByRole('dialog', { name: `URI rules of ${group}` });
      await expect(sheet).toBeVisible();
      await expect(sheet.getByText('No rules')).toBeVisible();
      await sheet.getByRole('button', { name: 'Add rule' }).click();
      await selectOption(sheet, 'Kind of rule 1', 'Template');
      await sheet.getByLabel('Pattern of rule 1').fill('/api/item/{id}/detail');
      await sheet.getByRole('button', { name: 'Add rule' }).click();
      await selectOption(sheet, 'Kind of rule 2', 'Prefix');
      await sheet.getByLabel('Pattern of rule 2').fill('/api/detail/');
      await expect(sheet.getByText('Unsaved changes')).toBeVisible();
      await sheet.getByRole('button', { name: 'Save' }).click();

      await expectToast(page, `2 rules saved for ${group}`);
      await sheet.getByRole('button', { name: 'Close' }).first().click();
      await expect(sheet).toBeHidden();

      const groupRow = groupsRegion.getByRole('row', { name: new RegExp(`^${group} `) });
      await expect(groupRow).toBeVisible();
      await expect(groupRow.getByRole('cell', { name: 'Item detail pages' })).toBeVisible();
      await expect(groupRow.getByRole('button', { name: '2', exact: true })).toBeVisible();

      // Reopening the editor from the row menu shows the saved rules.
      await groupRow.getByRole('button', { name: `Actions for ${group}` }).click();
      await page.getByRole('menuitem', { name: 'Edit URI rules' }).click();
      await expect(sheet.getByLabel('Pattern of rule 1')).toHaveValue('/api/item/{id}/detail');
      await sheet.getByRole('button', { name: 'Close' }).first().click();
      await expect(sheet).toBeHidden();

      // --- URI tester -------------------------------------------------------
      const uri = page.getByLabel('URI', { exact: true });
      await uri.fill('/api/item/4711/detail?ref=e2e');
      await page.getByRole('button', { name: 'Test', exact: true }).click();

      await expect(page.getByText('Matched rule')).toBeVisible();
      await expect(page.getByText('Template', { exact: true })).toBeVisible();
      // The effective policy set always resolves all four policy kinds.
      for (const kind of ['Rotation', 'Signal', 'Action', 'Breaker']) {
        await expect(page.getByRole('row', { name: new RegExp(`^${kind} `) })).toBeVisible();
      }

      // A URI that matches no rule falls back to _default.
      await uri.fill('/nothing/matches/here');
      await page.getByRole('button', { name: 'Test', exact: true }).click();
      await expect(page.getByText('No rule matched; the _default group applies.')).toBeVisible();

      // --- edit the site ----------------------------------------------------
      await page.getByRole('button', { name: 'Edit', exact: true }).click();
      const editForm = dialog(page);
      await expect(editForm.getByLabel('Name', { exact: true })).toBeDisabled();
      await editForm.getByLabel('Display name').fill('End-to-end site (edited)');
      await editForm.getByRole('button', { name: 'Save' }).click();
      await expectToast(page, `Site ${site} updated`);
      await expect(page.getByRole('heading', { level: 2, name: 'End-to-end site (edited)' })).toBeVisible();

      // --- delete the site --------------------------------------------------
      await page.getByRole('button', { name: 'Delete', exact: true }).click();
      const deleteDialog = page.getByRole('alertdialog');
      await expect(deleteDialog).toContainText('Delete site End-to-end site (edited)?');
      await deleteDialog.getByLabel(`Type ${site} to confirm`).fill(site);
      await deleteDialog.getByRole('button', { name: 'Delete' }).click();
      await expectToast(page, `Site ${site} deleted`);
      await expect(page.getByRole('navigation', { name: 'Sites' })).not.toContainText(site);
    } finally {
      await api.deleteSite(site);
    }
  });

  test('pauses and resumes a site with the site switch', async ({ page, api }) => {
    const site = uniqueName('pause');
    await api.createSite(site, { displayName: 'Pause drill' });

    try {
      await gotoPage(page, `/sites?site=${site}`, 'Sites');
      await expect(page.getByRole('heading', { level: 2, name: 'Pause drill' })).toBeVisible();

      const switchControl = page.getByRole('switch', { name: `Pause site ${site}` });
      await expect(switchControl).toBeChecked();
      await switchControl.click();

      const pauseDialog = page.getByRole('alertdialog');
      await expect(pauseDialog).toContainText(`Pause site ${site}?`);
      await pauseDialog.getByLabel('Reason').fill('console e2e drill');
      await pauseDialog.getByRole('button', { name: 'Pause site' }).click();
      await expectToast(page, `Site ${site} paused`);

      const resumeControl = page.getByRole('switch', { name: `Resume site ${site}` });
      await expect(resumeControl).not.toBeChecked();
      await expect(page.getByRole('navigation', { name: 'Sites' }).getByText('Paused')).toBeVisible();

      await resumeControl.click();
      await page.getByRole('alertdialog').getByRole('button', { name: 'Resume site' }).click();
      await expectToast(page, `Site ${site} resumed`);
      await expect(switchControl).toBeChecked();
    } finally {
      await api.deleteSite(site);
    }
  });
});
