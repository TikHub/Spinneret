import { NAMESPACE, SEEDED_SITE, uniqueName } from './fixtures/env';
import { expect, test } from './fixtures/test';
import { dialog, expectToast, gotoPage, selectOption } from './fixtures/ui';

test.describe('observability', () => {
  test('the overview shows the namespace totals, trend charts and one card per site', async ({ page }) => {
    await gotoPage(page, '/', 'Overview');

    const totals = page.locator('main section:not([data-page-intro])').first();
    for (const label of [
      'Available identities',
      'Acquire QPS',
      'Report QPS',
      'Success ratio',
      'Risk ratio',
      'Open breakers',
    ]) {
      await expect(totals.getByText(label, { exact: true })).toBeVisible();
    }

    await expect(page.getByRole('img', { name: 'Acquire rate (last hour)' })).toBeVisible();
    await expect(page.getByRole('img', { name: 'Success and risk ratio (last hour)' })).toBeVisible();

    // One card per site of the namespace.
    await expect(page.getByRole('heading', { level: 2, name: 'Sites' })).toBeVisible();
    await expect(page.getByText(SEEDED_SITE, { exact: true }).first()).toBeVisible();

    await expect(page.getByRole('heading', { level: 3, name: 'Open breakers' })).toBeVisible();
    await expect(page.getByRole('heading', { level: 3, name: 'Low watermark warnings' })).toBeVisible();
    await expect(page.getByRole('heading', { level: 2, name: /Nodes/ })).toBeVisible();

    // The window selector reloads the totals.
    await selectOption(page, 'Window', 'Last 1h');
    await expect(page.getByRole('combobox', { name: 'Window' })).toContainText('Last 1h');
  });

  test('the heatmap renders identities against endpoint groups of a seeded site', async ({ page }) => {
    await gotoPage(page, `/heatmap?site=${SEEDED_SITE}`, 'Cooldown heatmap');

    await expect(page.getByRole('toolbar', { name: 'Heatmap controls' })).toBeVisible();
    await expect(
      page.getByRole('img', { name: /Heatmap of \d+ identities and \d+ endpoint groups/ }),
    ).toBeVisible();

    // The table view is the accessible alternative of the chart.
    await page.getByRole('tab', { name: 'Table' }).click();
    await expect(page.getByRole('table')).toBeVisible();
    await expect(page.getByRole('row').nth(1)).toBeVisible();

    // The score metric is a second view of the same data.
    await page.getByRole('tab', { name: 'Score' }).click();
    await page.getByRole('tab', { name: 'Chart' }).click();
    await expect(page.getByRole('img', { name: /Heatmap of \d+ identities/ })).toBeVisible();
  });

  test('the request explorer lists ClickHouse events and filters them', async ({ page }) => {
    await gotoPage(page, '/requests', 'Request explorer');

    await expect(page.getByText('Matching events')).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Outcome distribution' })).toBeVisible();
    // The seeded traffic is inside the default range.
    await expect(page.getByRole('row').nth(1)).toBeVisible();

    await selectOption(page, 'Time range', 'Last 24 hours');
    await selectOption(page, /^Site/, new RegExp(`\\(${SEEDED_SITE}\\)$`));
    await expect(page).toHaveURL(/site=/);
    await expect(page.getByRole('row').nth(1)).toBeVisible();

    // An impossible filter shows the empty state instead of stale rows.
    await page.getByRole('textbox', { name: 'Node' }).fill('no-such-node-e2e');
    await expect(page.getByText('No request events')).toBeVisible();
  });

  test('a failed report shows up in the risk events list', async ({ page, api }) => {
    const site = uniqueName('risk');
    const tokenName = uniqueName('risknode');
    await api.createSite(site, { displayName: 'Risk drill' });
    await api.createIdentityType(
      site,
      `name: risk_token\nclient: web\nfields:\n  token: { type: string, required: true, sensitive: true }\nunique_by: [token]\nactivation: immediate\ndeliver:\n  headers:\n    Authorization: "Bearer {{ token }}"\n`,
    );
    // Several identities: one that was just leased is inside its reuse interval.
    await api.importIdentities(
      site,
      'risk_token',
      [1, 2, 3, 4].map((n) => ({ token: `${site}-token-${n}` })),
    );
    const token = await api.createNodeToken(tokenName);

    try {
      // One rate-limited request: a non-success report becomes a risk event.
      await api.acquireAndReport(token, site, { httpStatus: 429 });
      // Reports are processed asynchronously; wait until the event is stored.
      await expect
        .poll(
          async () => {
            const res = await api.call<{ events?: unknown[] }>('DashboardService/ListRiskEvents', {
              namespace: NAMESPACE,
              site,
              page_size: 5,
            });
            return res.events?.length ?? 0;
          },
          { timeout: 30_000 },
        )
        .toBeGreaterThan(0);

      await gotoPage(page, '/risk-events', 'Risk events');
      await selectOption(page, /^Site/, new RegExp(site));

      const row = page.getByRole('row').nth(1);
      await expect(row).toBeVisible();
      await expect(row).toContainText('429');
      await expect(page.getByText('Something went wrong')).toHaveCount(0);

      // The row expands into the full report.
      await row.getByRole('button').first().click();
      await expect(page.getByText('/api/search?q=e2e').first()).toBeVisible();
    } finally {
      await api.revokeTokenByName(tokenName);
      await api.deleteSite(site);
    }
  });

  test('the accounts page lists accounts of the namespace', async ({ page, api }) => {
    const site = uniqueName('acct');
    await api.createSite(site, { displayName: 'Account drill' });

    try {
      await gotoPage(page, '/accounts', 'Accounts');

      await page.getByRole('button', { name: 'Add account' }).click();
      const form = dialog(page);
      await expect(form.getByRole('heading', { name: 'Add account' })).toBeVisible();
      await selectOption(form, /^Site/, new RegExp(site));
      await form.getByLabel(/^External reference/).fill('e2e-user-1');
      await form.getByLabel('Notes').fill('Created by the console e2e suite');
      await form.getByRole('button', { name: 'Save' }).click();
      await expectToast(page, 'Account e2e-user-1 saved');

      const row = page.getByRole('row', { name: /e2e-user-1/ });
      await expect(row).toContainText(site);
      await expect(row).toContainText('Active');
    } finally {
      await api.deleteSite(site);
    }
  });
});
