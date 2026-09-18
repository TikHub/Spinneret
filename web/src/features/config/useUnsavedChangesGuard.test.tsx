import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { describe, expect, it } from 'vitest';

import { useUnsavedChangesRegistry } from '@/app/unsaved/useUnsavedChanges';
import { UnsavedChangesProvider } from '@/app/unsaved/UnsavedChangesProvider';

import { useUnsavedChangesGuard } from './useUnsavedChangesGuard';

function Editor() {
  const [dirty, setDirty] = useState(false);
  useUnsavedChangesGuard(dirty);
  const { hasUnsavedChanges } = useUnsavedChangesRegistry();
  const [reported, setReported] = useState('');
  return (
    <>
      <button type="button" onClick={() => setDirty((d) => !d)}>
        toggle
      </button>
      <button type="button" onClick={() => setReported(String(hasUnsavedChanges()))}>
        check
      </button>
      <output>{reported}</output>
    </>
  );
}

describe('useUnsavedChangesGuard', () => {
  it('registers the config draft with the scope-switch registry', async () => {
    const user = userEvent.setup();
    const router = createRouter({
      routeTree: createRootRoute({ component: Editor }),
      history: createMemoryHistory({ initialEntries: ['/'] }),
    });
    render(
      <UnsavedChangesProvider>
        {/* The test router is not the registered app router type. */}
        <RouterProvider router={router as never} />
      </UnsavedChangesProvider>,
    );

    await user.click(await screen.findByRole('button', { name: 'check' }));
    expect(screen.getByRole('status')).toHaveTextContent('false');
    await user.click(screen.getByRole('button', { name: 'toggle' }));
    await user.click(screen.getByRole('button', { name: 'check' }));
    expect(screen.getByRole('status')).toHaveTextContent('true');
    await user.click(screen.getByRole('button', { name: 'toggle' }));
    await user.click(screen.getByRole('button', { name: 'check' }));
    expect(screen.getByRole('status')).toHaveTextContent('false');
  });
});
