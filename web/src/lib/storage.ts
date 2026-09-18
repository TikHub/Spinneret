/** localStorage keys used by the console. */
export const STORAGE_KEYS = {
  tenant: 'spinneret.tenant',
  namespace: 'spinneret.namespace',
  theme: 'spinneret.theme',
  language: 'spinneret.lang',
  sidebarCollapsed: 'spinneret.sidebar.collapsed',
} as const;

/** Reads a string from localStorage; returns undefined when unavailable. */
export function readStorage(key: string): string | undefined {
  try {
    return window.localStorage.getItem(key) ?? undefined;
  } catch {
    return undefined;
  }
}

/** Writes (or removes, for undefined) a string in localStorage, ignoring quota and privacy errors. */
export function writeStorage(key: string, value: string | undefined): void {
  try {
    if (value === undefined) {
      window.localStorage.removeItem(key);
    } else {
      window.localStorage.setItem(key, value);
    }
  } catch {
    // Storage can be unavailable (private mode, quota); persistence is best effort.
  }
}
