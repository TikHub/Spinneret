import { useBlocker } from '@tanstack/react-router';
import { useCallback, useRef } from 'react';

import { useRegisterUnsavedChanges } from '@/app/unsaved/useUnsavedChanges';

/**
 * Blocks in-app navigation (including selecting another item) and page
 * unloads while there are unsaved changes, and asks before leaving. The draft
 * is also registered with the unsaved-changes registry, so tenant and
 * namespace switches (which do not navigate) ask too.
 */
export function useUnsavedChangesGuard(dirty: boolean) {
  useRegisterUnsavedChanges(dirty);
  const skipNext = useRef(false);
  const shouldBlockFn = useCallback(() => {
    if (skipNext.current) {
      skipNext.current = false;
      return false;
    }
    return dirty;
  }, [dirty]);
  const blocker = useBlocker({
    shouldBlockFn,
    enableBeforeUnload: dirty,
    withResolver: true,
  });
  /** Lets the next navigation through (e.g. after deleting the item). */
  const allowNextNavigation = useCallback(() => {
    skipNext.current = true;
  }, []);
  return { blocker, allowNextNavigation };
}

export type UnsavedChangesBlocker = ReturnType<typeof useUnsavedChangesGuard>['blocker'];
