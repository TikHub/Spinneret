import { uniqueName } from './fixtures/env';
import { expect, test } from './fixtures/test';
import { dialog, expectToast, gotoPage } from './fixtures/ui';

test.describe('tenants and namespaces', () => {
  test('creates a namespace and deletes it again', async ({ page, api }) => {
    const name = uniqueName('ns').slice(0, 63);

    try {
      await gotoPage(page, '/admin/tenants', 'Tenants & namespaces');
      await expect(page.getByRole('heading', { name: 'Namespaces of default' })).toBeVisible();

      await page.getByRole('button', { name: 'Create namespace' }).click();
      const form = dialog(page);
      await expect(form.getByRole('heading', { name: 'Create namespace' })).toBeVisible();
      await form.getByLabel(/^Name \(slug\)/).fill(name);
      await form.getByLabel('Display name').fill('E2E namespace');
      await form.getByLabel('Description').fill('Created by the console e2e suite');
      await form.getByRole('button', { name: 'Create' }).click();
      await expectToast(page, `Namespace ${name} created`);

      const row = page.getByRole('row', { name: new RegExp(name) });
      await expect(row).toContainText('E2E namespace');

      // It also appears in the namespace switcher of the shell.
      await page.getByTestId('namespace-switcher').click();
      await expect(page.getByRole('menu')).toContainText(name);
      await page.keyboard.press('Escape');

      // --- delete it again ----------------------------------------------------
      await row.getByRole('button', { name: 'Delete' }).click();
      const confirm = page.getByRole('alertdialog');
      await expect(confirm).toContainText(`Delete namespace ${name}?`);
      await confirm.getByLabel(`Type ${name} to confirm`).fill(name);
      await confirm.getByRole('button', { name: 'Delete' }).click();
      await expectToast(page, `Namespace ${name} deleted`);
      await expect(page.getByRole('row', { name: new RegExp(name) })).toHaveCount(0);
    } finally {
      const list = await api.tryCall<{ namespaces?: { id: string; name: string }[] }>(
        'TenantAdminService/ListNamespaces',
        { tenant_id: api.tenantId, page_size: 500 },
      );
      const leftover = list?.namespaces?.find((n) => n.name === name);
      if (leftover) await api.tryCall('TenantAdminService/DeleteNamespace', { id: leftover.id });
    }
  });

  test('creates a tenant and deletes it again', async ({ page, api }) => {
    const name = uniqueName('ten').slice(0, 63);

    try {
      await gotoPage(page, '/admin/tenants', 'Tenants & namespaces');

      await page.getByRole('button', { name: 'Create tenant' }).click();
      const form = dialog(page);
      await expect(form.getByRole('heading', { name: 'Create tenant' })).toBeVisible();
      await form.getByLabel(/^Name \(slug\)/).fill(name);
      await form.getByLabel('Display name').fill('E2E tenant');
      await form.getByRole('button', { name: 'Create' }).click();
      await expectToast(page, `Tenant ${name} created`);

      const row = page.getByRole('row', { name: new RegExp(name) });
      await expect(row).toContainText('E2E tenant');
      await expect(row.getByRole('button', { name: 'Switch to tenant' })).toBeEnabled();

      await row.getByRole('button', { name: 'Delete' }).click();
      const confirm = page.getByRole('alertdialog');
      await expect(confirm).toContainText(`Delete tenant ${name}?`);
      await confirm.getByLabel(`Type ${name} to confirm`).fill(name);
      await confirm.getByRole('button', { name: 'Delete' }).click();
      await expectToast(page, `Tenant ${name} deleted`);
    } finally {
      const list = await api.tryCall<{ tenants?: { id: string; name: string }[] }>(
        'TenantAdminService/ListTenants',
        { page_size: 500 },
      );
      const leftover = list?.tenants?.find((t) => t.name === name);
      if (leftover) await api.tryCall('TenantAdminService/DeleteTenant', { id: leftover.id });
    }
  });
});
