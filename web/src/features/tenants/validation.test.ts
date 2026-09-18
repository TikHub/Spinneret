import { describe, expect, it } from 'vitest';

import { EMPTY_ENTITY_FORM, entityUpdateInit, suggestSlug, validateEntityForm } from './validation';

describe('validateEntityForm', () => {
  it('requires a valid slug on create only', () => {
    expect(validateEntityForm(EMPTY_ENTITY_FORM, 'tenant', 'create')).toEqual({ name: 'required' });
    expect(validateEntityForm(EMPTY_ENTITY_FORM, 'tenant', 'edit')).toEqual({});
    for (const name of ['a', 'Acme', '-acme', 'acme_team', 'a'.repeat(64), 'ac me']) {
      expect(validateEntityForm({ ...EMPTY_ENTITY_FORM, name }, 'namespace', 'create').name).toBe('pattern');
    }
    for (const name of ['ab', 'acme', 'growth-team', '0prod', 'a'.repeat(63)]) {
      expect(validateEntityForm({ ...EMPTY_ENTITY_FORM, name }, 'namespace', 'create')).toEqual({});
    }
  });

  it('limits display name and description lengths', () => {
    const errors = validateEntityForm(
      { ...EMPTY_ENTITY_FORM, name: 'acme', displayName: 'x'.repeat(129), description: 'y'.repeat(1025) },
      'tenant',
      'create',
    );
    expect(errors).toEqual({ displayName: 'tooLong', description: 'tooLong' });
  });

  it('validates the optional owner user ID for new tenants', () => {
    const base = { ...EMPTY_ENTITY_FORM, name: 'acme' };
    expect(validateEntityForm({ ...base, ownerUserId: 'usr_1' }, 'tenant', 'create')).toEqual({});
    expect(validateEntityForm({ ...base, ownerUserId: 'usr 1' }, 'tenant', 'create').ownerUserId).toBe(
      'invalid',
    );
    expect(validateEntityForm({ ...base, ownerUserId: 'u'.repeat(65) }, 'tenant', 'create').ownerUserId).toBe(
      'tooLong',
    );
    expect(validateEntityForm({ ...base, ownerUserId: 'usr 1' }, 'namespace', 'create')).toEqual({});
  });
});

describe('suggestSlug', () => {
  it('derives a valid slug from a display name', () => {
    expect(suggestSlug('Growth Team')).toBe('growth-team');
    expect(suggestSlug('  --Prod / EU--  ')).toBe('prod-eu');
    expect(suggestSlug('数据')).toBe('');
    expect(suggestSlug(`${'a'.repeat(62)} b`)).toBe('a'.repeat(62));
  });
});

describe('entityUpdateInit', () => {
  it('includes only changed fields', () => {
    const original = { displayName: 'Acme', description: 'Main tenant' };
    const values = { ...EMPTY_ENTITY_FORM, name: 'acme', ...original };
    expect(entityUpdateInit(original, values)).toEqual({});
    expect(entityUpdateInit(original, { ...values, description: '' })).toEqual({ description: '' });
  });
});
