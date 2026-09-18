import { type ApiToken } from '@/gen/spinneret/v1/access_admin_pb';
import { parseDuration } from '@/lib/duration';
import { toDate } from '@/lib/time';

import { isIpOrCidr } from './ip';
import { MAX_SCOPES, type ScopeRow, scopesFromRows, validateScopeRows } from './scopes';

/** Lifecycle status of an API token. */
export type TokenStatus = 'active' | 'expired' | 'revoked';

export function tokenStatus(token: ApiToken, now: number): TokenStatus {
  if (toDate(token.revokedAt)) return 'revoked';
  const expires = toDate(token.expiresAt);
  if (expires && expires.getTime() <= now) return 'expired';
  return 'active';
}

/** Expiry presets for new tokens. */
export const TOKEN_EXPIRY_PRESETS = ['1d', '7d', '30d', '90d', '180d', '365d'] as const;

/** CreateTokenRequest limits. */
export const MAX_TOKEN_NAME_LENGTH = 64;
export const MAX_TOKEN_DESCRIPTION_LENGTH = 1024;
export const MAX_IP_ALLOWLIST = 256;
export const MAX_RATE_LIMIT_RPS = 1_000_000;

/** Values of the create token form. */
export interface TokenFormValues {
  name: string;
  description: string;
  scopes: ScopeRow[];
  ipAllowlist: string[];
  /** Requests per second as typed; empty or "0" means unlimited. */
  rateLimit: string;
  /** Duration until expiry ("90d"); empty means the token never expires. */
  expiresIn: string;
}

export const EMPTY_TOKEN_FORM: TokenFormValues = {
  name: '',
  description: '',
  scopes: [],
  ipAllowlist: [],
  rateLimit: '',
  expiresIn: '90d',
};

export type TokenFormErrors = Partial<{
  name: 'required' | 'tooLong';
  description: 'tooLong';
  scopes: 'required' | 'tooMany' | 'invalidRows';
  ipAllowlist: 'invalid' | 'tooMany';
  rateLimit: 'invalid';
  expiresIn: 'invalid' | 'notPositive';
}>;

/** Parses the rate limit input; undefined when invalid. */
export function parseRateLimit(value: string): number | undefined {
  const trimmed = value.trim();
  if (trimmed === '') return 0;
  if (!/^\d+$/.test(trimmed)) return undefined;
  const rps = Number(trimmed);
  return rps <= MAX_RATE_LIMIT_RPS ? rps : undefined;
}

/**
 * Resolves the expiry input to an absolute time: undefined for "never",
 * 'invalid' / 'notPositive' for unusable input.
 */
export function resolveExpiry(value: string, now: number): Date | undefined | 'invalid' | 'notPositive' {
  if (value.trim() === '') return undefined;
  const parsed = parseDuration(value);
  if (!parsed || parsed.permanent) return 'invalid';
  if (parsed.ms <= 0) return 'notPositive';
  return new Date(now + parsed.ms);
}

export function validateTokenForm(values: TokenFormValues, now: number): TokenFormErrors {
  const errors: TokenFormErrors = {};
  const name = values.name.trim();
  if (name === '') errors.name = 'required';
  else if (name.length > MAX_TOKEN_NAME_LENGTH) errors.name = 'tooLong';
  if (values.description.length > MAX_TOKEN_DESCRIPTION_LENGTH) errors.description = 'tooLong';

  if (values.scopes.length === 0) errors.scopes = 'required';
  else if (Object.keys(validateScopeRows(values.scopes)).length > 0) errors.scopes = 'invalidRows';
  else if (scopesFromRows(values.scopes).length > MAX_SCOPES) errors.scopes = 'tooMany';

  if (values.ipAllowlist.length > MAX_IP_ALLOWLIST) errors.ipAllowlist = 'tooMany';
  else if (!values.ipAllowlist.every(isIpOrCidr)) errors.ipAllowlist = 'invalid';

  if (parseRateLimit(values.rateLimit) === undefined) errors.rateLimit = 'invalid';

  const expiry = resolveExpiry(values.expiresIn, now);
  if (expiry === 'invalid' || expiry === 'notPositive') errors.expiresIn = expiry;
  return errors;
}
