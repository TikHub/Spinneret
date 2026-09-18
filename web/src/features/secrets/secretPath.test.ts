import { describe, expect, it } from 'vitest';

import {
  buildSecretFolders,
  inFolder,
  isValidSecretTag,
  splitSecretPath,
  validateSecretPath,
} from './secretPath';

describe('validateSecretPath', () => {
  it('accepts canonical paths', () => {
    for (const path of ['key', 'signing/api_key', 'a.b/c-d/e_f', '0/1', `a${'b'.repeat(255)}`]) {
      expect(validateSecretPath(path)).toBeUndefined();
    }
  });

  it('rejects empty and pattern violations', () => {
    expect(validateSecretPath('')).toBe('required');
    expect(validateSecretPath('Upper/case')).toBe('pattern');
    expect(validateSecretPath('/leading')).toBe('pattern');
    expect(validateSecretPath('.hidden')).toBe('pattern');
    expect(validateSecretPath('has space')).toBe('pattern');
    expect(validateSecretPath(`a${'b'.repeat(256)}`)).toBe('pattern');
  });

  it('rejects empty, "." and ".." segments', () => {
    expect(validateSecretPath('a//b')).toBe('segments');
    expect(validateSecretPath('a/')).toBe('segments');
    expect(validateSecretPath('a/./b')).toBe('segments');
    expect(validateSecretPath('a/../b')).toBe('segments');
    expect(validateSecretPath('a/..')).toBe('segments');
    // Dots inside a segment are fine.
    expect(validateSecretPath('a/..b/c.')).toBeUndefined();
  });
});

describe('secret folders', () => {
  it('splits paths', () => {
    expect(splitSecretPath('a/b/c')).toEqual({ folder: 'a/b/', name: 'c' });
    expect(splitSecretPath('c')).toEqual({ folder: '', name: 'c' });
  });

  it('builds a sorted folder tree with recursive counts', () => {
    const tree = buildSecretFolders([
      'root',
      'signing/api/key',
      'signing/cookie',
      'vendor/x',
      'signing/api/sign',
    ]);
    expect(tree).toEqual([
      {
        prefix: 'signing/',
        name: 'signing',
        count: 3,
        children: [{ prefix: 'signing/api/', name: 'api', count: 2, children: [] }],
      },
      { prefix: 'vendor/', name: 'vendor', count: 1, children: [] },
    ]);
  });

  it('matches folder prefixes', () => {
    expect(inFolder('signing/api/key', 'signing/')).toBe(true);
    expect(inFolder('x/signing/api_key', 'signing/')).toBe(false);
    expect(inFolder('anything', '')).toBe(true);
  });

  it('validates tags', () => {
    expect(isValidSecretTag('prod')).toBe(true);
    expect(isValidSecretTag('')).toBe(false);
    expect(isValidSecretTag('t'.repeat(65))).toBe(false);
  });
});
