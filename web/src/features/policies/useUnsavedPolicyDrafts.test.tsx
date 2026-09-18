import { renderHook } from '@testing-library/react';
import { type ReactNode } from 'react';
import { describe, expect, it } from 'vitest';

import { useUnsavedChangesRegistry } from '@/app/unsaved/useUnsavedChanges';
import { UnsavedChangesProvider } from '@/app/unsaved/UnsavedChangesProvider';

import { useUnsavedPolicyDrafts } from './useUnsavedPolicyDrafts';

const wrapper = ({ children }: { children: ReactNode }) => (
  <UnsavedChangesProvider>{children}</UnsavedChangesProvider>
);

function useDraftsWithRegistry(scope: string) {
  return { unsaved: useUnsavedPolicyDrafts(scope), registry: useUnsavedChangesRegistry() };
}

describe('useUnsavedPolicyDrafts', () => {
  it('reports drafts of any policy to the scope-switch registry', () => {
    const { result } = renderHook(() => useDraftsWithRegistry('ten_1/default'), { wrapper });
    expect(result.current.registry.hasUnsavedChanges()).toBe(false);
    // Editors write drafts into the map without re-rendering the page.
    result.current.unsaved.set('pol_1', 'kind: rotation');
    expect(result.current.registry.hasUnsavedChanges()).toBe(true);
    result.current.unsaved.delete('pol_1');
    expect(result.current.registry.hasUnsavedChanges()).toBe(false);
  });

  it('starts empty in a new scope', () => {
    const { result, rerender } = renderHook(({ scope }) => useDraftsWithRegistry(scope), {
      wrapper,
      initialProps: { scope: 'ten_1/default' },
    });
    result.current.unsaved.set('pol_1', 'kind: rotation');
    rerender({ scope: 'ten_1/staging' });
    expect(result.current.unsaved.size).toBe(0);
    expect(result.current.registry.hasUnsavedChanges()).toBe(false);
  });

  it('warns before unloading the page with drafts', () => {
    const { result } = renderHook(() => useDraftsWithRegistry('ten_1/default'), { wrapper });
    const clean = new Event('beforeunload', { cancelable: true });
    window.dispatchEvent(clean);
    expect(clean.defaultPrevented).toBe(false);
    result.current.unsaved.set('pol_1', 'kind: rotation');
    const dirty = new Event('beforeunload', { cancelable: true });
    window.dispatchEvent(dirty);
    expect(dirty.defaultPrevented).toBe(true);
  });
});
