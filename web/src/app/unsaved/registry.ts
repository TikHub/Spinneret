import { createContext } from 'react';

/** Reports whether an editor currently holds unsaved changes. */
export type DirtyCheck = () => boolean;

/**
 * Set of dirty checks of the mounted editors. Actions that would discard
 * editor state without a navigation (tenant or namespace switches) ask it
 * before running.
 */
export interface UnsavedChangesRegistry {
  /** Adds a check; the returned function removes it (idempotent). */
  register: (check: DirtyCheck) => () => void;
  /** True when any registered check reports unsaved changes. */
  isDirty: () => boolean;
}

export function createUnsavedChangesRegistry(): UnsavedChangesRegistry {
  // Entries wrap the checks so the same function can be registered twice.
  const entries = new Set<{ check: DirtyCheck }>();
  return {
    register: (check) => {
      const entry = { check };
      entries.add(entry);
      return () => {
        entries.delete(entry);
      };
    },
    isDirty: () => {
      for (const entry of entries) {
        if (entry.check()) return true;
      }
      return false;
    },
  };
}

export interface UnsavedChangesContextValue {
  registry: UnsavedChangesRegistry;
  /** Resolves true when nothing is dirty or the user chose to discard the changes. */
  confirmDiscard: () => Promise<boolean>;
}

/** Provided by UnsavedChangesProvider; null outside of it. */
export const UnsavedChangesContext = createContext<UnsavedChangesContextValue | null>(null);
