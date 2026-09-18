import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  useSyncExternalStore,
} from 'react';
import { type ReactNode } from 'react';

import { readStorage, STORAGE_KEYS, writeStorage } from '@/lib/storage';

export type ThemePreference = 'light' | 'dark' | 'system';
export type ResolvedTheme = 'light' | 'dark';

export interface ThemeContextValue {
  /** The user's choice. */
  theme: ThemePreference;
  /** The theme actually applied (system resolved). */
  resolvedTheme: ResolvedTheme;
  setTheme: (theme: ThemePreference) => void;
}

const ThemeContext = createContext<ThemeContextValue | undefined>(undefined);

const DARK_QUERY = '(prefers-color-scheme: dark)';

function subscribeSystemTheme(onChange: () => void): () => void {
  if (typeof window === 'undefined' || !window.matchMedia) return () => undefined;
  const media = window.matchMedia(DARK_QUERY);
  media.addEventListener('change', onChange);
  return () => media.removeEventListener('change', onChange);
}

function getSystemTheme(): ResolvedTheme {
  if (typeof window === 'undefined' || !window.matchMedia) return 'light';
  return window.matchMedia(DARK_QUERY).matches ? 'dark' : 'light';
}

function readPreference(): ThemePreference {
  const stored = readStorage(STORAGE_KEYS.theme);
  return stored === 'light' || stored === 'dark' || stored === 'system' ? stored : 'system';
}

/** Applies light/dark/system theme via the `dark` class on <html> and persists the choice. */
export function ThemeProvider({ children }: { children: ReactNode }) {
  const [theme, setThemeState] = useState<ThemePreference>(readPreference);
  const systemTheme = useSyncExternalStore(subscribeSystemTheme, getSystemTheme, () => 'light' as const);
  const resolvedTheme: ResolvedTheme = theme === 'system' ? systemTheme : theme;

  useEffect(() => {
    const root = document.documentElement;
    root.classList.toggle('dark', resolvedTheme === 'dark');
    root.style.colorScheme = resolvedTheme;
  }, [resolvedTheme]);

  const setTheme = useCallback((next: ThemePreference) => {
    writeStorage(STORAGE_KEYS.theme, next);
    setThemeState(next);
  }, []);

  const value = useMemo(() => ({ theme, resolvedTheme, setTheme }), [theme, resolvedTheme, setTheme]);
  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>;
}

/** Returns the theme context; must be used inside ThemeProvider. */
// eslint-disable-next-line react-refresh/only-export-components
export function useTheme(): ThemeContextValue {
  const ctx = useContext(ThemeContext);
  if (!ctx) throw new Error('useTheme must be used inside ThemeProvider');
  return ctx;
}
