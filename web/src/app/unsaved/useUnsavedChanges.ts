import { useContext, useEffect, useLayoutEffect, useMemo, useRef } from 'react';

import { UnsavedChangesContext, type DirtyCheck } from './registry';

/**
 * Registers the unsaved changes of an editor while the calling component is
 * mounted, so tenant and namespace switches ask before discarding them. Pass
 * the dirty flag, or a function for state that React does not track (e.g. a
 * mutable map of drafts). Without an UnsavedChangesProvider it does nothing.
 */
export function useRegisterUnsavedChanges(dirty: boolean | DirtyCheck): void {
  const registry = useContext(UnsavedChangesContext)?.registry;
  const latest = useRef(dirty);
  useLayoutEffect(() => {
    latest.current = dirty;
  });
  useEffect(() => {
    if (!registry) return undefined;
    return registry.register(() => {
      const current = latest.current;
      return typeof current === 'function' ? current() : current;
    });
  }, [registry]);
}

export interface UnsavedChangesGuard {
  /** True while a registered editor has unsaved changes. */
  hasUnsavedChanges: () => boolean;
  /** Resolves true when nothing is dirty or the user chose to discard the changes. */
  confirmDiscard: () => Promise<boolean>;
  /** Runs `action` right away when nothing is dirty, otherwise only after the user confirms discarding. */
  runAfterDiscardConfirmed: (action: () => void) => void;
}

const NO_GUARD: UnsavedChangesGuard = {
  hasUnsavedChanges: () => false,
  confirmDiscard: () => Promise.resolve(true),
  runAfterDiscardConfirmed: (action) => action(),
};

/** Access to the unsaved-changes registry for actions that discard editor state (scope switches). */
export function useUnsavedChangesRegistry(): UnsavedChangesGuard {
  const context = useContext(UnsavedChangesContext);
  return useMemo(() => {
    if (!context) return NO_GUARD;
    const { registry, confirmDiscard } = context;
    return {
      hasUnsavedChanges: registry.isDirty,
      confirmDiscard,
      runAfterDiscardConfirmed: (action) => {
        if (!registry.isDirty()) {
          action();
          return;
        }
        void confirmDiscard().then((discard) => {
          if (discard) action();
        });
      },
    };
  }, [context]);
}
