import { create } from '@bufbuild/protobuf';
import { describe, expect, it } from 'vitest';

import { ApiTokenSchema } from '@/gen/spinneret/v1/access_admin_pb';
import { DAY_MS, toTimestamp } from '@/lib/time';

import { hasErrors } from './forms';
import { createScopeRow } from './scopes';
import { EMPTY_TOKEN_FORM, parseRateLimit, resolveExpiry, tokenStatus, validateTokenForm } from './tokens';

const now = Date.UTC(2026, 8, 17);

describe('tokenStatus', () => {
  it('prefers revoked over expired', () => {
    const token = create(ApiTokenSchema, {
      expiresAt: toTimestamp(now - 1000),
      revokedAt: toTimestamp(now - 2000),
    });
    expect(tokenStatus(token, now)).toBe('revoked');
    expect(tokenStatus(create(ApiTokenSchema, { expiresAt: toTimestamp(now - 1) }), now)).toBe('expired');
    expect(tokenStatus(create(ApiTokenSchema, { expiresAt: toTimestamp(now + 1) }), now)).toBe('active');
    expect(tokenStatus(create(ApiTokenSchema, {}), now)).toBe('active');
  });
});

describe('token form', () => {
  const valid = { ...EMPTY_TOKEN_FORM, name: 'crawler-01', scopes: [createScopeRow('lease:acquire')] };

  it('accepts a valid form', () => {
    expect(hasErrors(validateTokenForm(valid, now))).toBe(false);
  });

  it('requires a name and at least one valid scope', () => {
    const errors = validateTokenForm({ ...EMPTY_TOKEN_FORM, name: ' ' }, now);
    expect(errors).toMatchObject({ name: 'required', scopes: 'required' });
    expect(validateTokenForm({ ...valid, scopes: [createScopeRow('secret:read')] }, now).scopes).toBe(
      'invalidRows',
    );
  });

  it('validates the IP allowlist, rate limit and expiry', () => {
    const errors = validateTokenForm(
      { ...valid, ipAllowlist: ['10.0.0.0/8', 'nope'], rateLimit: '1.5', expiresIn: 'permanent' },
      now,
    );
    expect(errors).toEqual({ ipAllowlist: 'invalid', rateLimit: 'invalid', expiresIn: 'invalid' });
    expect(validateTokenForm({ ...valid, expiresIn: '0' }, now).expiresIn).toBe('notPositive');
  });

  it('parses the rate limit', () => {
    expect(parseRateLimit('')).toBe(0);
    expect(parseRateLimit('250')).toBe(250);
    expect(parseRateLimit('-1')).toBeUndefined();
    expect(parseRateLimit('1000001')).toBeUndefined();
  });

  it('resolves the expiry relative to now', () => {
    expect(resolveExpiry('', now)).toBeUndefined();
    expect(resolveExpiry('90d', now)).toEqual(new Date(now + 90 * DAY_MS));
    expect(resolveExpiry('soon', now)).toBe('invalid');
  });
});
