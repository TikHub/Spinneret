import { describe, expect, it } from 'vitest';

import enConfig from '@/i18n/locales/en/config.json';
import enNotifications from '@/i18n/locales/en/notifications.json';
import enSecrets from '@/i18n/locales/en/secrets.json';
import zhConfig from '@/i18n/locales/zh-CN/config.json';
import zhNotifications from '@/i18n/locales/zh-CN/notifications.json';
import zhSecrets from '@/i18n/locales/zh-CN/secrets.json';

function keys(value: unknown, prefix = ''): string[] {
  if (value === null || typeof value !== 'object') return [prefix];
  return Object.entries(value).flatMap(([k, v]) => keys(v, prefix ? `${prefix}.${k}` : k));
}

describe.each([
  ['config', enConfig, zhConfig],
  ['secrets', enSecrets, zhSecrets],
  ['notifications', enNotifications, zhNotifications],
])('%s locales', (_name, en, zh) => {
  it('have identical key sets in en and zh-CN', () => {
    expect(keys(zh).sort()).toEqual(keys(en).sort());
  });

  it('have no empty translations', () => {
    const empty = (bundle: unknown) =>
      keys(bundle).filter((key) => {
        const value = key
          .split('.')
          .reduce<unknown>((node, part) => (node as Record<string, unknown>)[part], bundle);
        return typeof value !== 'string' || value.trim() === '';
      });
    expect(empty(en)).toEqual([]);
    expect(empty(zh)).toEqual([]);
  });
});
