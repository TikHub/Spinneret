import { act, render, renderHook, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState, type ReactNode } from 'react';
import { describe, expect, it, vi } from 'vitest';

import '@/i18n';

import { createUnsavedChangesRegistry } from './registry';
import { useRegisterUnsavedChanges, useUnsavedChangesRegistry } from './useUnsavedChanges';
import { UnsavedChangesProvider } from './UnsavedChangesProvider';

describe('createUnsavedChangesRegistry', () => {
  it('is dirty while any registered check reports unsaved changes', () => {
    const registry = createUnsavedChangesRegistry();
    expect(registry.isDirty()).toBe(false);
    let editorDirty = false;
    const unregisterEditor = registry.register(() => editorDirty);
    const unregisterClean = registry.register(() => false);
    expect(registry.isDirty()).toBe(false);
    editorDirty = true;
    expect(registry.isDirty()).toBe(true);
    unregisterEditor();
    expect(registry.isDirty()).toBe(false);
    // Unregistering twice is harmless.
    unregisterEditor();
    unregisterClean();
    expect(registry.isDirty()).toBe(false);
  });
});

/** An editor whose dirty flag the test toggles, plus a scope switch button. */
function Workspace({ onSwitch }: { onSwitch: () => void }) {
  const [dirty, setDirty] = useState(false);
  const [mounted, setMounted] = useState(true);
  return (
    <>
      {mounted && <Editor dirty={dirty} />}
      <button type="button" onClick={() => setDirty((d) => !d)}>
        toggle dirty
      </button>
      <button type="button" onClick={() => setMounted(false)}>
        close editor
      </button>
      <SwitchButton onSwitch={onSwitch} />
    </>
  );
}

function Editor({ dirty }: { dirty: boolean }) {
  useRegisterUnsavedChanges(dirty);
  return <p>editor</p>;
}

function SwitchButton({ onSwitch }: { onSwitch: () => void }) {
  const { runAfterDiscardConfirmed } = useUnsavedChangesRegistry();
  return (
    <button type="button" onClick={() => runAfterDiscardConfirmed(onSwitch)}>
      switch scope
    </button>
  );
}

function renderWorkspace() {
  const onSwitch = vi.fn();
  render(
    <UnsavedChangesProvider>
      <Workspace onSwitch={onSwitch} />
    </UnsavedChangesProvider>,
  );
  return onSwitch;
}

describe('UnsavedChangesProvider', () => {
  it('runs the action right away when no editor has unsaved changes', async () => {
    const user = userEvent.setup();
    const onSwitch = renderWorkspace();
    await user.click(screen.getByRole('button', { name: 'switch scope' }));
    expect(onSwitch).toHaveBeenCalledOnce();
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument();
  });

  it('asks before discarding unsaved changes and runs the action only on discard', async () => {
    const user = userEvent.setup();
    const onSwitch = renderWorkspace();
    await user.click(screen.getByRole('button', { name: 'toggle dirty' }));

    await user.click(screen.getByRole('button', { name: 'switch scope' }));
    const dialog = await screen.findByRole('alertdialog', { name: 'Discard unsaved changes?' });
    expect(dialog).toHaveTextContent('switching the tenant or namespace');
    await user.click(screen.getByRole('button', { name: 'Keep editing' }));
    await waitFor(() => expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument());
    expect(onSwitch).not.toHaveBeenCalled();

    await user.click(screen.getByRole('button', { name: 'switch scope' }));
    await user.click(await screen.findByRole('button', { name: 'Discard changes' }));
    await waitFor(() => expect(onSwitch).toHaveBeenCalledOnce());
    await waitFor(() => expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument());
    expect(document.body.style.pointerEvents).not.toBe('none');
  });

  it('stops asking once the dirty editor unmounts', async () => {
    const user = userEvent.setup();
    const onSwitch = renderWorkspace();
    await user.click(screen.getByRole('button', { name: 'toggle dirty' }));
    await user.click(screen.getByRole('button', { name: 'close editor' }));
    await user.click(screen.getByRole('button', { name: 'switch scope' }));
    expect(onSwitch).toHaveBeenCalledOnce();
  });

  it('reads the latest state of a getter registration', async () => {
    const drafts = new Map<string, string>();
    const wrapper = ({ children }: { children: ReactNode }) => (
      <UnsavedChangesProvider>{children}</UnsavedChangesProvider>
    );
    const { result } = renderHook(
      () => {
        useRegisterUnsavedChanges(() => drafts.size > 0);
        return useUnsavedChangesRegistry();
      },
      { wrapper },
    );
    expect(result.current.hasUnsavedChanges()).toBe(false);
    drafts.set('pol_1', 'kind: rotation');
    expect(result.current.hasUnsavedChanges()).toBe(true);

    let confirmed: Promise<boolean> | undefined;
    act(() => {
      confirmed = result.current.confirmDiscard();
    });
    expect(await screen.findByRole('alertdialog')).toBeInTheDocument();
    await userEvent.setup().click(screen.getByRole('button', { name: 'Keep editing' }));
    await expect(confirmed).resolves.toBe(false);
  });

  it('lets everything through without a provider', async () => {
    const { result } = renderHook(() => {
      useRegisterUnsavedChanges(true);
      return useUnsavedChangesRegistry();
    });
    expect(result.current.hasUnsavedChanges()).toBe(false);
    await expect(result.current.confirmDiscard()).resolves.toBe(true);
  });
});
