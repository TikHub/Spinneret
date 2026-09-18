import { describe, expect, it } from 'vitest';

import { safeRedirect } from './redirect';

describe('safeRedirect', () => {
  it('keeps same-origin paths', () => {
    expect(safeRedirect('/identities?site=a')).toBe('/identities?site=a');
    expect(safeRedirect('/access/tokens')).toBe('/access/tokens');
  });

  it.each([
    undefined,
    '',
    'https://evil.example',
    '//evil.example',
    '/\\evil.example',
    '/\t/evil.example',
    '/\n/evil.example',
    '/login/',
    'identities',
    '/login',
    '/login?redirect=/',
  ])('rejects %j', (target) => {
    expect(safeRedirect(target)).toBe('/');
  });
});
