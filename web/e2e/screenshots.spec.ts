import { mkdirSync } from 'node:fs';
import { dirname, resolve } from 'node:path';

import { SEEDED_SITE } from './fixtures/env';
import { expect, test } from './fixtures/test';
import { editorText, gotoPage, selectOption } from './fixtures/ui';

/**
 * Screenshots for the README, taken at the suite viewport (1440x900) in the
 * light theme and English (a Chinese overview shows the second language).
 */
const OUT_DIR = resolve(process.cwd(), '..', 'documents', 'images');

function shot(name: string): string {
  const path = resolve(OUT_DIR, `${name}.png`);
  mkdirSync(dirname(path), { recursive: true });
  return path;
}

test.describe('documentation screenshots', () => {
  test('captures every console page used in the README', async ({ page, api }) => {
    test.setTimeout(300_000);

    // Fresh traffic so the dashboards are not a wall of zeros.
    const tokenName = `e2e-screenshots-${Date.now().toString(36)}`;
    const token = await api.createNodeToken(tokenName);
    const sent = await api.seedTraffic(token, SEEDED_SITE, 40);
    expect(sent).toBeGreaterThan(0);
    await api.revokeTokenByName(tokenName);

    // --- overview -----------------------------------------------------------
    await gotoPage(page, '/', 'Overview');
    await expect(page.getByRole('img', { name: 'Acquire rate (last hour)' })).toBeVisible();
    await expect(page.getByText(SEEDED_SITE, { exact: true }).first()).toBeVisible();
    // The per-minute aggregation lags a few seconds behind the reports.
    const totals = page.locator('main section:not([data-page-intro])').first();
    await expect
      .poll(
        async () => {
          await page.getByRole('button', { name: 'Refresh' }).click();
          return totals.innerText();
        },
        { timeout: 120_000, intervals: [5_000] },
      )
      .not.toMatch(/Acquire QPS\s*0\/s/);
    await page.screenshot({ path: shot('overview') });

    // --- overview in Chinese --------------------------------------------------
    await page.getByTestId('language-switcher').click();
    await page.getByTestId('language-zh-CN').click();
    await expect(page.getByRole('heading', { level: 1, name: '概览' })).toBeVisible();
    await expect(page.getByRole('img', { name: '获取速率（最近 1 小时）' })).toBeVisible();
    await page.screenshot({ path: shot('overview-zh') });
    await page.getByTestId('language-switcher').click();
    await page.getByTestId('language-en').click();
    await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible();

    // --- identities -------------------------------------------------------------
    await gotoPage(page, `/identities?site=${SEEDED_SITE}`, 'Identities');
    await expect(page.getByRole('row').nth(1)).toBeVisible();
    await page.screenshot({ path: shot('identities') });

    // --- identity detail -----------------------------------------------------------
    const firstId = await page.getByRole('row').nth(1).getByRole('link').first().innerText();
    await page.getByRole('link', { name: firstId }).click();
    await expect(page.getByRole('heading', { level: 1 })).toContainText(firstId);
    await expect(page.getByText(/field masked|Payload/).first()).toBeVisible();
    await page.screenshot({ path: shot('identity-detail') });

    // --- policies editor ---------------------------------------------------------------
    await gotoPage(page, '/policies', 'Policies');
    await page.getByRole('button', { name: /^default-rotation/ }).click();
    await expect(page.getByRole('heading', { level: 2, name: 'default-rotation' })).toBeVisible();
    await page.getByRole('group', { name: 'Editor mode' }).getByRole('button', { name: 'Form' }).click();
    await expect(page.getByLabel('Max concurrent leases')).toBeVisible();
    await page.screenshot({ path: shot('policies') });

    // --- breakers ---------------------------------------------------------------------
    await gotoPage(page, '/breakers', 'Breakers');
    await expect(page.getByRole('row').nth(1)).toBeVisible();
    await page.screenshot({ path: shot('breakers') });

    // --- config center -------------------------------------------------------------------
    await gotoPage(page, '/config', 'Config Center');
    const item = page.getByRole('button', { name: /^example\.json/ });
    if (await item.isVisible().catch(() => false)) {
      await item.click();
      await expect(page.getByRole('tab', { name: 'Content' })).toBeVisible();
      // Monaco is lazily loaded; without this the screenshot shows an empty box.
      await expect.poll(() => editorText(page, 'Content')).toContain('{');
    }
    await page.screenshot({ path: shot('config') });

    // --- heatmap -------------------------------------------------------------------------
    await gotoPage(page, `/heatmap?site=${SEEDED_SITE}`, 'Cooldown heatmap');
    await expect(page.getByRole('img', { name: /Heatmap of \d+ identities/ })).toBeVisible();
    await page.screenshot({ path: shot('heatmap') });

    // --- request explorer ------------------------------------------------------------------
    await gotoPage(page, '/requests', 'Request explorer');
    await selectOption(page, 'Time range', 'Last 24 hours');
    await expect(page.getByRole('row').nth(1)).toBeVisible();
    // The summary tiles and the chart render after their query; without this the
    // screenshot catches their skeletons.
    await expect(page.getByRole('img', { name: 'Outcome distribution' })).toBeVisible();
    await expect.poll(() => page.locator('main').innerText()).toMatch(/Matching events\s*[\d.,]/);
    await page.screenshot({ path: shot('requests') });
  });
});
