import { create } from '@bufbuild/protobuf';
import { describe, expect, it } from 'vitest';

import { ROLE_PERMISSIONS } from '@/app/auth/permissions';
import { UserSchema } from '@/gen/spinneret/v1/auth_pb';

import {
  EMPTY_BINDING,
  EMPTY_CREATE_USER,
  editValuesFromUser,
  hasUserChanges,
  passwordStrength,
  redundantExtraPermissions,
  toBindingInit,
  userUpdateInit,
  validateBinding,
  validateCreateUser,
  validateEditUser,
  validateEmail,
  validatePassword,
  withNamespace,
} from './userForm';

describe('binding form', () => {
  it('requires a namespace when sites are chosen', () => {
    expect(validateBinding({ ...EMPTY_BINDING, sites: ['shop'] })).toEqual({ sites: 'requireNamespace' });
    expect(validateBinding({ ...EMPTY_BINDING, namespace: 'prod', sites: ['shop'] })).toEqual({});
    expect(validateBinding({ ...EMPTY_BINDING, namespace: '  ', sites: ['shop'] })).toEqual({
      sites: 'requireNamespace',
    });
  });

  it('rejects duplicate sites, unknown roles and invalid extra permissions', () => {
    expect(validateBinding({ ...EMPTY_BINDING, namespace: 'prod', sites: ['a', 'a'] })).toEqual({
      sites: 'duplicate',
    });
    expect(validateBinding({ ...EMPTY_BINDING, role: 'root' as never })).toEqual({ role: 'invalid' });
    expect(validateBinding({ ...EMPTY_BINDING, extraPermissions: ['user:write' as never] })).toEqual({
      extraPermissions: 'invalid',
    });
  });

  it('clears sites when the namespace changes', () => {
    const values = { ...EMPTY_BINDING, namespace: 'prod', sites: ['shop'] };
    expect(withNamespace(values, 'prod')).toBe(values);
    expect(withNamespace(values, 'staging')).toEqual({ ...values, namespace: 'staging', sites: [] });
    expect(withNamespace(values, '')).toEqual({ ...values, namespace: '', sites: [] });
  });

  it('never sends sites without a namespace', () => {
    expect(toBindingInit({ role: 'operator', namespace: '', sites: ['x'], extraPermissions: [] })).toEqual({
      role: 'operator',
      namespace: '',
      sites: [],
      extraPermissions: [],
    });
    expect(
      toBindingInit({
        role: 'viewer',
        namespace: ' prod ',
        sites: ['x'],
        extraPermissions: ['secret:reveal'],
      }),
    ).toEqual({ role: 'viewer', namespace: 'prod', sites: ['x'], extraPermissions: ['secret:reveal'] });
  });

  it('reports extra permissions already included in the role', () => {
    expect(redundantExtraPermissions('admin', ['config:publish', 'secret:reveal'], ROLE_PERMISSIONS)).toEqual(
      ['config:publish', 'secret:reveal'],
    );
    expect(redundantExtraPermissions('viewer', ['config:publish'], ROLE_PERMISSIONS)).toEqual([]);
  });
});

describe('create user form', () => {
  const valid = {
    ...EMPTY_CREATE_USER,
    username: 'ops.alice',
    password: 'correct-horse-battery',
  };

  it('accepts a valid user', () => {
    expect(validateCreateUser(valid)).toEqual({});
  });

  it('validates the username pattern', () => {
    expect(validateCreateUser({ ...valid, username: '' }).username).toBe('required');
    expect(validateCreateUser({ ...valid, username: 'ab' }).username).toBe('pattern');
    expect(validateCreateUser({ ...valid, username: 'Alice' }).username).toBe('pattern');
    expect(validateCreateUser({ ...valid, username: '.alice' }).username).toBe('pattern');
    expect(validateCreateUser({ ...valid, username: 'a'.repeat(65) }).username).toBe('pattern');
    expect(validateCreateUser({ ...valid, username: 'a'.repeat(64) }).username).toBeUndefined();
  });

  it('validates password, email and binding fields together', () => {
    const errors = validateCreateUser({
      ...valid,
      password: 'short',
      email: 'not-an-email',
      binding: { ...EMPTY_BINDING, sites: ['shop'] },
    });
    expect(errors).toEqual({ password: 'tooShort', email: 'invalid', sites: 'requireNamespace' });
  });

  it('checks password length and email format', () => {
    expect(validatePassword('')).toBe('required');
    expect(validatePassword('123456789')).toBe('tooShort');
    expect(validatePassword('1234567890')).toBeUndefined();
    expect(validatePassword('x'.repeat(1025))).toBe('tooLong');
    expect(validateEmail('')).toBeUndefined();
    expect(validateEmail('ops@example.com')).toBeUndefined();
    expect(validateEmail('ops@example')).toBe('invalid');
  });

  it('rates password strength', () => {
    expect(passwordStrength('short')).toBe(0);
    expect(passwordStrength('aaaaaaaaaaaa')).toBe(0);
    expect(passwordStrength('abcdefghijk')).toBe(1);
    expect(passwordStrength('abcdefghij1')).toBe(2);
    expect(passwordStrength('Abcdefghij1')).toBe(3);
    expect(passwordStrength('Abcdefghij1!Abcdefghij1!')).toBe(4);
  });
});

describe('edit user form', () => {
  const user = create(UserSchema, {
    id: 'usr_1',
    username: 'ops',
    displayName: 'Ops',
    email: 'ops@example.com',
    locale: 'en',
  });

  it('sends only changed fields', () => {
    const values = editValuesFromUser(user);
    expect(userUpdateInit(user, values)).toEqual({ id: 'usr_1' });
    expect(hasUserChanges(userUpdateInit(user, values))).toBe(false);
    const changed = userUpdateInit(user, { ...values, email: '', disabled: true, locale: 'zh-CN' });
    expect(changed).toEqual({ id: 'usr_1', email: '', disabled: true, locale: 'zh-CN' });
    expect(hasUserChanges(changed)).toBe(true);
  });

  it('validates the locale and email', () => {
    const values = editValuesFromUser(user);
    expect(validateEditUser(values)).toEqual({});
    expect(validateEditUser({ ...values, locale: 'zh_CN', email: 'x' })).toEqual({
      locale: 'invalid',
      email: 'invalid',
    });
  });
});
