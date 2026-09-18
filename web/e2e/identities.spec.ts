import { uniqueName } from './fixtures/env';
import { expect, test } from './fixtures/test';
import { dialog, expectToast, gotoPage, selectOption } from './fixtures/ui';

/** Identity type used by the import journey: one sensitive field, immediate activation. */
const TYPE_SPEC = `name: e2e_token
client: web
description: Console e2e tokens
fields:
  token: { type: string, required: true, sensitive: true }
unique_by: [token]
activation: immediate
deliver:
  headers:
    Authorization: "Bearer {{ token }}"
`;

const ROWS = [
  { token: 'e2e-token-alpha', _labels: { name: 'alpha' }, _tags: ['e2e'], _region: 'eu' },
  { token: 'e2e-token-beta', _labels: { name: 'beta' }, _tags: ['e2e'] },
  { token: 'e2e-token-gamma', _labels: { name: 'gamma' }, _tags: ['e2e'] },
];

test.describe('identities', () => {
  test('imports identities (dry run then import), filters them and operates on one', async ({
    page,
    api,
  }) => {
    const site = uniqueName('ident');
    await api.createSite(site, { displayName: 'Identity drill' });
    await api.createIdentityType(site, TYPE_SPEC);

    try {
      await gotoPage(page, '/identities', 'Identities');

      // --- import: dry run first ---------------------------------------------
      await page.getByRole('button', { name: 'Import' }).click();
      const importDialog = dialog(page);
      await expect(importDialog.getByRole('heading', { name: 'Import identities' })).toBeVisible();
      await selectOption(importDialog, /^Site/, new RegExp(site));
      await selectOption(importDialog, /^Identity type/, 'e2e_token');
      await importDialog.getByRole('tab', { name: 'Paste' }).click();
      await importDialog
        .getByRole('textbox', { name: 'Paste' })
        .fill(ROWS.map((row) => JSON.stringify(row)).join('\n'));

      await importDialog.getByRole('button', { name: 'Dry run' }).click();
      const result = importDialog.getByRole('region', { name: 'Import result' });
      await expect(result).toContainText('Dry run: nothing was stored');
      await expect(result).toContainText('3 rows processed');
      await expect(result).toContainText('Would create');
      await expectToast(page, /Dry run passed: 3 to create/);

      // --- import ---------------------------------------------------------------
      await importDialog.getByRole('button', { name: 'Import', exact: true }).click();
      await expect(result).toContainText('Import finished');
      await expectToast(page, /Imported: 3 created/);
      await importDialog.getByRole('button', { name: 'Close' }).first().click();

      // --- filter by site --------------------------------------------------------
      await selectOption(page, /^Site/, new RegExp(site));
      await expect(page.getByRole('row').filter({ hasText: 'e2e_token' })).toHaveCount(3);
      await expect(page).toHaveURL(new RegExp(`site=${site}`));

      // The free-text filter narrows the list further.
      const search = page.getByRole('textbox', { name: 'ID prefix or label value…' });
      await search.fill('alpha');
      await expect(page.getByRole('row').filter({ hasText: 'e2e_token' })).toHaveCount(1);
      await search.fill('');
      await expect(page.getByRole('row').filter({ hasText: 'e2e_token' })).toHaveCount(3);

      // --- open the detail page ---------------------------------------------------
      const firstId = await page
        .getByRole('row')
        .filter({ hasText: 'e2e_token' })
        .first()
        .getByRole('link')
        .innerText();
      await page.getByRole('link', { name: firstId }).click();
      const heading = page.getByRole('heading', { level: 1 });
      await expect(heading).toContainText(firstId);
      await expect(heading).toContainText('Active');

      // --- cooldown -----------------------------------------------------------------
      await page.getByRole('button', { name: 'Operations' }).click();
      await page.getByRole('menuitem', { name: 'Cooldown' }).click();
      const cooldown = page.getByRole('alertdialog');
      await expect(cooldown).toContainText('Cooldown 1 identity');
      await selectOption(cooldown, 'Scope', 'Whole site');
      await cooldown.getByLabel('Duration').fill('30m');
      await cooldown.getByLabel('Reason').fill('console e2e cooldown');
      await cooldown.getByRole('button', { name: 'Cooldown', exact: true }).click();
      await expectToast(page, /Cooldown: 1 identity updated/);
      await expect(page.getByText('console e2e cooldown').first()).toBeVisible();

      // --- ban and unban --------------------------------------------------------------
      await page.getByRole('button', { name: 'Operations' }).click();
      await page.getByRole('menuitem', { name: 'Ban', exact: true }).click();
      const ban = page.getByRole('alertdialog');
      await ban.getByLabel('Duration').fill('7d');
      await ban.getByLabel('Reason').fill('console e2e ban');
      await ban.getByRole('button', { name: 'Ban', exact: true }).click();
      await expectToast(page, /Ban: 1 identity updated/);
      await expect(heading).toContainText('Banned');

      await page.getByRole('button', { name: 'Operations' }).click();
      await page.getByRole('menuitem', { name: 'Unban' }).click();
      await page.getByRole('alertdialog').getByRole('button', { name: 'Unban', exact: true }).click();
      await expectToast(page, /Unban: 1 identity updated/);
      // Unbanning returns the identity to pending, so it is re-validated before use.
      await expect(heading).toContainText('Pending');
      await expect(heading).not.toContainText('Banned');
      // The lifecycle transitions are recorded in the state event timeline.
      await expect(page.getByText('console e2e ban').first()).toBeVisible();

      // --- reveal the payload ------------------------------------------------------------
      const payload = page.getByRole('region', { name: 'Payload' }).or(page.locator('body'));
      await expect(page.getByText(/field masked/)).toBeVisible();
      await page.getByRole('button', { name: 'Reveal' }).click();
      const revealDialog = page.getByRole('alertdialog');
      await expect(revealDialog).toContainText('Reveal sensitive payload?');
      await revealDialog.getByLabel('Type reveal to confirm').fill('reveal');
      await revealDialog.getByRole('button', { name: 'Reveal' }).click();
      await expect(payload.getByText(/e2e-token-/).first()).toBeVisible();
      await expect(page.getByRole('button', { name: 'Hide', exact: true })).toBeVisible();
    } finally {
      await api.deleteSite(site);
    }
  });
});
