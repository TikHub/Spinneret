import { describe, expect, it } from 'vitest';

import { searchName } from './siteSearch';

describe('searchName', () => {
  it('reads strings and numeric names parsed from hand-typed URLs', () => {
    expect(searchName('shop')).toBe('shop');
    expect(searchName(123)).toBe('123');
  });

  it('ignores empty and non-name values', () => {
    expect(searchName('')).toBeUndefined();
    expect(searchName(undefined)).toBeUndefined();
    expect(searchName(true)).toBeUndefined();
    expect(searchName(['shop'])).toBeUndefined();
    expect(searchName(1.5)).toBeUndefined();
    expect(searchName(-1)).toBeUndefined();
  });
});
