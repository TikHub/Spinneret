import i18n, { type BackendModule, type ReadCallback } from 'i18next';
import { initReactI18next } from 'react-i18next';

import { readStorage, STORAGE_KEYS, writeStorage } from '@/lib/storage';

import enCommon from './locales/en/common.json';
import zhCommon from './locales/zh-CN/common.json';

export const SUPPORTED_LANGUAGES = ['en', 'zh-CN'] as const;
export type Language = (typeof SUPPORTED_LANGUAGES)[number];
export const DEFAULT_LANGUAGE: Language = 'en';

/** Native language names shown in the language switcher. */
export const LANGUAGE_NAMES: Record<Language, string> = {
  en: 'English',
  'zh-CN': '简体中文',
};

/** Normalizes a locale tag to a supported language, or undefined. */
export function toSupportedLanguage(tag: string | null | undefined): Language | undefined {
  if (!tag) return undefined;
  const lower = tag.toLowerCase();
  if (lower.startsWith('zh')) return 'zh-CN';
  if (lower.startsWith('en')) return 'en';
  return undefined;
}

/** Stored choice, then the browser language, then English. */
export function detectLanguage(): Language {
  const stored = toSupportedLanguage(readStorage(STORAGE_KEYS.language));
  if (stored) return stored;
  const browser = typeof navigator === 'undefined' ? undefined : navigator.language;
  return toSupportedLanguage(browser) ?? DEFAULT_LANGUAGE;
}

// Feature namespaces (every locales/<lang>/<ns>.json except common) are split into
// lazily loaded chunks and fetched on first use of useTranslation('<ns>').
const lazyResources = import.meta.glob<{ default: Record<string, unknown> }>([
  './locales/*/*.json',
  '!./locales/*/common.json',
]);

const lazyBackend: BackendModule = {
  type: 'backend',
  init: () => undefined,
  read(language: string, namespace: string, callback: ReadCallback) {
    const loader = lazyResources[`./locales/${language}/${namespace}.json`];
    if (!loader) {
      callback(null, {});
      return;
    }
    loader().then(
      (mod) => callback(null, mod.default),
      (err: unknown) => callback(err instanceof Error ? err : new Error(String(err)), null),
    );
  },
};

void i18n
  .use(lazyBackend)
  .use(initReactI18next)
  .init({
    resources: {
      en: { common: enCommon },
      'zh-CN': { common: zhCommon },
    },
    partialBundledLanguages: true,
    lng: detectLanguage(),
    fallbackLng: DEFAULT_LANGUAGE,
    supportedLngs: [...SUPPORTED_LANGUAGES],
    load: 'currentOnly',
    ns: ['common'],
    defaultNS: 'common',
    fallbackNS: 'common',
    interpolation: { escapeValue: false },
    returnNull: false,
    react: { useSuspense: true },
  });

function applyLanguage(language: string): void {
  if (typeof document !== 'undefined') {
    document.documentElement.lang = language;
  }
}

applyLanguage(i18n.language);
i18n.on('languageChanged', (language) => {
  writeStorage(STORAGE_KEYS.language, language);
  applyLanguage(language);
});

export default i18n;
