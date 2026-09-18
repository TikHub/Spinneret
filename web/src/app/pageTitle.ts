import { useEffect, useSyncExternalStore } from 'react';

type Listener = () => void;

let override: string | undefined;
const listeners = new Set<Listener>();

function setOverride(title: string | undefined): void {
  if (override === title) return;
  override = title;
  listeners.forEach((l) => l());
}

function subscribe(listener: Listener): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

/** Current page title override (set by usePageTitle). */
export function usePageTitleOverride(): string | undefined {
  return useSyncExternalStore(
    subscribe,
    () => override,
    () => override,
  );
}

/**
 * Overrides the document title of the current page (e.g. with an entity name).
 * Without it the title comes from the route's staticData.titleKey.
 */
export function usePageTitle(title: string | undefined): void {
  useEffect(() => {
    setOverride(title);
    return () => setOverride(undefined);
  }, [title]);
}
