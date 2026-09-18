import { uniqueName } from './fixtures/env';
import { expect, test } from './fixtures/test';
import { dialog, expectToast, gotoPage, selectOption } from './fixtures/ui';

test.describe('notifications', () => {
  test('creates a webhook channel, sends a test alert and reads the alert history', async ({ page, api }) => {
    const name = uniqueName('hook');

    try {
      await gotoPage(page, '/notifications', 'Notifications');

      // --- create a webhook channel -------------------------------------------
      await page.getByRole('button', { name: 'New channel' }).click();
      const form = dialog(page);
      await expect(form.getByRole('heading', { name: 'New notification channel' })).toBeVisible();
      await form.getByLabel(/^Name/).fill(name);
      await selectOption(form, 'Kind', 'Webhook');
      await selectOption(form, 'Min severity', 'Info');
      // 127.0.0.1:9 discards connections, so the test delivery fails quickly and
      // the failure state is what this journey checks.
      await form.getByLabel(/^URL/).fill('http://127.0.0.1:9/e2e');
      // Event types are picked in a popover; the defaults already cover the
      // operational alerts, so only the test event is added here.
      await form.getByLabel('Event types').click();
      const events = page.getByRole('dialog').getByRole('checkbox', { name: /^Test / });
      await events.check();
      await expect(form.getByRole('checkbox', { name: /^Breaker opened/ })).toBeChecked();
      await page.keyboard.press('Escape');
      await form.getByRole('button', { name: 'Create' }).click();
      await expectToast(page, `Channel ${name} created`);

      const row = page.getByRole('row', { name: new RegExp(name) });
      await expect(row).toBeVisible();
      await expect(row).toContainText('Webhook');
      await expect(row).toContainText('Never');

      // --- send a test alert (the endpoint is unreachable on purpose) ------------
      await row.getByRole('button', { name: 'Send test alert' }).click();
      await expectToast(page, new RegExp(`Test (alert delivered through|delivery through) ${name}`));
      await expect(row.getByRole('img', { name: /Delivery failed|Delivered successfully/ })).toBeVisible();

      // --- the alert history records the delivery ---------------------------------
      await page.getByRole('tab', { name: 'Alert history' }).click();
      await expect(page.getByRole('row', { name: /Test/ }).first()).toBeVisible();
      await page.getByRole('tab', { name: 'Channels' }).click();

      // --- disable through the row switch -------------------------------------------
      await row.getByRole('switch', { name: `Enable channel ${name}` }).click();
      await expectToast(page, `Channel ${name} disabled`);

      // --- delete -------------------------------------------------------------------
      await row.getByRole('button', { name: 'Delete' }).click();
      const confirm = page.getByRole('alertdialog');
      await expect(confirm).toContainText(`Delete channel ${name}?`);
      await confirm.getByLabel(`Type ${name} to confirm`).fill(name);
      await confirm.getByRole('button', { name: 'Delete' }).click();
      await expectToast(page, `Channel ${name} deleted`);
      await expect(page.getByRole('row', { name: new RegExp(name) })).toHaveCount(0);
    } finally {
      const list = await api.tryCall<{ channels?: { id: string; name: string }[] }>(
        'NotificationAdminService/ListChannels',
        { page_size: 200 },
      );
      const channel = list?.channels?.find((c) => c.name === name);
      if (channel) await api.tryCall('NotificationAdminService/DeleteChannel', { id: channel.id });
    }
  });
});
