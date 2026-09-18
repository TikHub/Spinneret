import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import '@/i18n';

import { useRegisterUnsavedChanges } from '@/app/unsaved/useUnsavedChanges';
import { UnsavedChangesProvider } from '@/app/unsaved/UnsavedChangesProvider';

const auth = vi.hoisted(() => ({ setTenant: vi.fn(), setNamespace: vi.fn() }));

vi.mock('@/app/auth/AuthContext', async () => {
  const { create: make } = await import('@bufbuild/protobuf');
  const pb = await import('@/gen/spinneret/v1/auth_pb');
  const namespace = (id: string, name: string) =>
    make(pb.NamespaceAccessSchema, { namespace: make(pb.NamespaceSchema, { id, tenantId: 'ten_1', name }) });
  const namespaces = [namespace('ns_1', 'prod'), namespace('ns_2', 'staging')];
  const tenants = [
    make(pb.TenantAccessSchema, { tenant: make(pb.TenantSchema, { id: 'ten_1', name: 'acme' }), namespaces }),
    make(pb.TenantAccessSchema, { tenant: make(pb.TenantSchema, { id: 'ten_2', name: 'globex' }) }),
  ];
  return {
    useAuth: () => ({
      tenants,
      tenant: tenants[0],
      namespaces,
      namespaceName: 'prod',
      setTenant: auth.setTenant,
      setNamespace: auth.setNamespace,
    }),
  };
});

const { TenantSwitcher } = await import('./TenantSwitcher');
const { NamespaceSwitcher } = await import('./NamespaceSwitcher');

function DirtyEditor() {
  const [dirty, setDirty] = useState(false);
  useRegisterUnsavedChanges(dirty);
  return (
    <button type="button" onClick={() => setDirty(true)}>
      edit
    </button>
  );
}

function renderTopbar() {
  render(
    <UnsavedChangesProvider>
      <TenantSwitcher />
      <NamespaceSwitcher />
      <DirtyEditor />
    </UnsavedChangesProvider>,
  );
}

describe('scope switchers', () => {
  beforeEach(() => {
    auth.setTenant.mockReset();
    auth.setNamespace.mockReset();
  });

  it('switch right away without unsaved changes', async () => {
    const user = userEvent.setup();
    renderTopbar();
    await user.click(screen.getByTestId('tenant-switcher'));
    await user.click(await screen.findByRole('menuitem', { name: /globex/ }));
    expect(auth.setTenant).toHaveBeenCalledWith('ten_2');

    await user.click(screen.getByTestId('namespace-switcher'));
    await user.click(await screen.findByRole('menuitem', { name: /staging/ }));
    expect(auth.setNamespace).toHaveBeenCalledWith('staging');
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument();
  });

  it('ask before a tenant switch discards unsaved changes', async () => {
    const user = userEvent.setup();
    renderTopbar();
    await user.click(screen.getByRole('button', { name: 'edit' }));

    await user.click(screen.getByTestId('tenant-switcher'));
    await user.click(await screen.findByRole('menuitem', { name: /globex/ }));
    expect(await screen.findByRole('alertdialog', { name: 'Discard unsaved changes?' })).toBeInTheDocument();
    expect(auth.setTenant).not.toHaveBeenCalled();
    await user.click(screen.getByRole('button', { name: 'Keep editing' }));
    await waitFor(() => expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument());
    expect(auth.setTenant).not.toHaveBeenCalled();

    await user.click(screen.getByTestId('tenant-switcher'));
    await user.click(await screen.findByRole('menuitem', { name: /globex/ }));
    await user.click(await screen.findByRole('button', { name: 'Discard changes' }));
    await waitFor(() => expect(auth.setTenant).toHaveBeenCalledWith('ten_2'));
    await waitFor(() => expect(document.body.style.pointerEvents).not.toBe('none'));
  });

  it('ask before a namespace switch discards unsaved changes', async () => {
    const user = userEvent.setup();
    renderTopbar();
    await user.click(screen.getByRole('button', { name: 'edit' }));

    await user.click(screen.getByTestId('namespace-switcher'));
    await user.click(await screen.findByRole('menuitem', { name: /staging/ }));
    await user.click(await screen.findByRole('button', { name: 'Discard changes' }));
    await waitFor(() => expect(auth.setNamespace).toHaveBeenCalledWith('staging'));
  });

  it('do not ask when the current scope is selected again', async () => {
    const user = userEvent.setup();
    renderTopbar();
    await user.click(screen.getByRole('button', { name: 'edit' }));
    await user.click(screen.getByTestId('namespace-switcher'));
    await user.click(await screen.findByRole('menuitem', { name: /prod/ }));
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument();
    expect(auth.setNamespace).not.toHaveBeenCalled();
  });
});
