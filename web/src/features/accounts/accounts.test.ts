import { describe, expect, it } from 'vitest';

import {
  availableAccountOperations,
  buildOperateAccountRequest,
  buildUpsertAccountRequest,
  defaultAccountOperationForm,
  isPermanentAccountBan,
  mergeAccountSearch,
  parseAccountSearch,
  validateAccountOperation,
  validateUpsertAccount,
} from './accounts';

describe('account search params', () => {
  it('parses and validates state', () => {
    expect(parseAccountSearch({ site: ' shop ', state: 'banned', search: 42 })).toEqual({
      site: 'shop',
      state: 'banned',
      search: '42',
    });
    expect(parseAccountSearch({ state: 'retired' }).state).toBe('');
  });

  it('merges into search params dropping empty values', () => {
    expect(mergeAccountSearch({ tab: 'x', site: 'a' }, { site: '', state: 'active', search: '' })).toEqual({
      tab: 'x',
      site: undefined,
      state: 'active',
      search: undefined,
    });
  });
});

describe('account operations', () => {
  it('builds ban, cooldown and enable requests', () => {
    expect(
      buildOperateAccountRequest('acc_1', {
        ...defaultAccountOperationForm('ban'),
        duration: 'Permanent',
        reason: ' fraud ',
      }),
    ).toEqual({ id: 'acc_1', operation: 'ban', duration: 'permanent', reason: 'fraud' });
    expect(buildOperateAccountRequest('acc_1', defaultAccountOperationForm('cooldown'))).toEqual({
      id: 'acc_1',
      operation: 'cooldown',
      duration: '30m',
      reason: '',
    });
    expect(
      buildOperateAccountRequest('acc_1', { ...defaultAccountOperationForm('enable'), duration: '1h' })
        .duration,
    ).toBe('');
  });

  it('validates durations per operation', () => {
    expect(validateAccountOperation({ operation: 'cooldown', duration: 'permanent', reason: '' })).toEqual({
      duration: 'permanentNotAllowed',
    });
    expect(validateAccountOperation({ operation: 'ban', duration: '', reason: '' })).toEqual({
      duration: 'required',
    });
    expect(validateAccountOperation({ operation: 'unban', duration: '', reason: '' })).toEqual({});
    expect(isPermanentAccountBan({ operation: 'ban', duration: 'permanent', reason: '' })).toBe(true);
  });

  it('offers state-appropriate operations', () => {
    expect(availableAccountOperations('banned')).toEqual(['unban', 'ban']);
    expect(availableAccountOperations('disabled')).toEqual(['enable', 'ban']);
    expect(availableAccountOperations('active')).toEqual(['cooldown', 'ban', 'disable']);
  });
});

describe('upsert account', () => {
  it('validates and builds the request', () => {
    const form = { site: 'shop', externalRef: ' user-1 ', region: ' US ', tags: ['vip', ' '], notes: 'n' };
    expect(validateUpsertAccount(form)).toEqual({});
    expect(buildUpsertAccountRequest('default', form)).toEqual({
      namespace: 'default',
      site: 'shop',
      externalRef: 'user-1',
      region: 'US',
      tags: ['vip'],
      notes: 'n',
    });
    expect(validateUpsertAccount({ ...form, site: '', externalRef: '' })).toEqual({
      site: 'required',
      externalRef: 'required',
    });
  });
});
