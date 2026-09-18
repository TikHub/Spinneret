import { create } from '@bufbuild/protobuf';
import { describe, expect, it } from 'vitest';

import { SecretInfoSchema } from '@/gen/spinneret/v1/secret_admin_pb';
import { toTimestamp } from '@/lib/time';

import {
  createSecretRequest,
  EMPTY_SECRET_FORM,
  formFromSecret,
  hasErrors,
  updateSecretRequest,
  validateSecretForm,
} from './secretForm';
import { toDateTimeLocal } from './secretTime';

const NOW = new Date(2026, 8, 17, 12, 0).getTime();
const FUTURE = new Date(2026, 9, 1, 8, 30);

describe('validateSecretForm', () => {
  it('requires a valid path and a value on create', () => {
    const errors = validateSecretForm(EMPTY_SECRET_FORM, 'create', NOW);
    expect(errors).toEqual({ path: 'required', value: 'required' });
    expect(hasErrors(errors)).toBe(true);
    expect(validateSecretForm({ ...EMPTY_SECRET_FORM, path: 'a//b', value: 'x' }, 'create', NOW)).toEqual({
      path: 'segments',
    });
  });

  it('makes the value optional on edit and limits its size', () => {
    expect(validateSecretForm(EMPTY_SECRET_FORM, 'edit', NOW)).toEqual({});
    const big = { ...EMPTY_SECRET_FORM, value: 'x'.repeat(64 * 1024 + 1) };
    expect(validateSecretForm(big, 'edit', NOW).value).toBe('tooLarge');
  });

  it('validates tags and expiry', () => {
    const base = { ...EMPTY_SECRET_FORM, path: 'a/b', value: 'v' };
    expect(validateSecretForm({ ...base, tags: [''] }, 'create', NOW).tags).toBe('invalid');
    expect(validateSecretForm({ ...base, expires: true }, 'create', NOW).expiresAt).toBe('required');
    expect(validateSecretForm({ ...base, expires: true, expiresAt: 'nope' }, 'create', NOW).expiresAt).toBe(
      'invalid',
    );
    const past = toDateTimeLocal(NOW - 60_000);
    expect(validateSecretForm({ ...base, expires: true, expiresAt: past }, 'create', NOW).expiresAt).toBe(
      'past',
    );
    // An unchanged past expiry does not block editing other fields.
    const initial = { ...base, expires: true, expiresAt: past };
    expect(validateSecretForm(initial, 'edit', NOW, initial).expiresAt).toBeUndefined();
  });
});

describe('createSecretRequest', () => {
  it('builds the request with an optional expiry', () => {
    const values = {
      ...EMPTY_SECRET_FORM,
      path: 'signing/api_key',
      value: 'v',
      description: ' d ',
      tags: ['prod'],
      expires: true,
      expiresAt: toDateTimeLocal(FUTURE),
    };
    const request = createSecretRequest('prod-ns', values);
    expect(request).toMatchObject({
      namespace: 'prod-ns',
      path: 'signing/api_key',
      value: 'v',
      description: 'd',
      tags: ['prod'],
    });
    expect(request.expiresAt).toEqual(toTimestamp(FUTURE));
    expect(createSecretRequest('ns', { ...values, expires: false }).expiresAt).toBeUndefined();
  });
});

describe('updateSecretRequest', () => {
  const secret = create(SecretInfoSchema, {
    id: 'sec_1',
    path: 'signing/api_key',
    description: 'desc',
    tags: ['a', 'b'],
    expiresAt: toTimestamp(FUTURE),
  });

  it('returns undefined when nothing changed', () => {
    expect(updateSecretRequest(secret, formFromSecret(secret))).toBeUndefined();
  });

  it('sends only changed fields', () => {
    const form = formFromSecret(secret);
    expect(updateSecretRequest(secret, { ...form, value: 'new' })).toEqual({ id: 'sec_1', value: 'new' });
    expect(updateSecretRequest(secret, { ...form, tags: [] })).toEqual({
      id: 'sec_1',
      tags: [],
      setTags: true,
    });
    expect(updateSecretRequest(secret, { ...form, description: 'other' })).toEqual({
      id: 'sec_1',
      description: 'other',
    });
  });

  it('sets or clears the expiry', () => {
    const form = formFromSecret(secret);
    expect(updateSecretRequest(secret, { ...form, expires: false })).toEqual({
      id: 'sec_1',
      clearExpiresAt: true,
    });
    const later = new Date(2026, 10, 1, 0, 0);
    const request = updateSecretRequest(secret, { ...form, expiresAt: toDateTimeLocal(later) });
    expect(request?.clearExpiresAt).toBeUndefined();
    expect(request?.expiresAt).toEqual(toTimestamp(later));

    const noExpiry = create(SecretInfoSchema, { id: 'sec_2', path: 'x' });
    expect(updateSecretRequest(noExpiry, { ...formFromSecret(noExpiry), expires: false })).toBeUndefined();
  });
});
