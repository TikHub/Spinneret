import { Code, ConnectError } from '@connectrpc/connect';
import { describe, expect, it } from 'vitest';

import {
  codeName,
  describeError,
  errorMessage,
  errorReason,
  isKnownReason,
  isPermissionDenied,
  KNOWN_REASONS,
  retryAfterMs,
  shouldRetryQuery,
  toApiError,
} from './errors';

function apiError(code: Code, message: string, reason?: string, retryAfter?: string): ConnectError {
  const metadata = new Headers();
  if (reason) metadata.set('Spinneret-Reason', reason);
  if (retryAfter !== undefined) metadata.set('Spinneret-Retry-After-Ms', retryAfter);
  return new ConnectError(message, code, metadata);
}

const t = (key: string, options?: Record<string, unknown>) =>
  options && Object.keys(options).length > 0 ? `${key}|${JSON.stringify(options)}` : key;

describe('toApiError', () => {
  it('reads reason and retry-after from metadata (case-insensitive headers)', () => {
    const err = apiError(Code.ResourceExhausted, 'too many attempts', 'login_throttled', '12500');
    const api = toApiError(err);
    expect(api).toMatchObject({
      code: Code.ResourceExhausted,
      codeName: 'resource_exhausted',
      reason: 'login_throttled',
      message: 'too many attempts',
      retryAfterMs: 12500,
    });
    expect(errorReason(err)).toBe('login_throttled');
    expect(retryAfterMs(err)).toBe(12500);
  });

  it('ignores invalid retry hints and missing reasons', () => {
    const api = toApiError(apiError(Code.Internal, 'boom', undefined, 'soon'));
    expect(api.reason).toBeUndefined();
    expect(api.retryAfterMs).toBeUndefined();
  });

  it('wraps non-Connect errors', () => {
    const api = toApiError(new TypeError('Failed to fetch'));
    expect(api.code).toBe(Code.Unknown);
    expect(api.message).toBe('Failed to fetch');
  });
});

describe('codeName', () => {
  it('converts Connect codes to snake_case', () => {
    expect(codeName(Code.PermissionDenied)).toBe('permission_denied');
    expect(codeName(Code.Unauthenticated)).toBe('unauthenticated');
    expect(codeName(Code.DeadlineExceeded)).toBe('deadline_exceeded');
  });
});

describe('describeError', () => {
  it('maps known reasons to translated messages with retry seconds', () => {
    const d = describeError(apiError(Code.ResourceExhausted, 'slow down', 'rate_limited', '1500'), t);
    expect(d.title).toBe(
      'errors.reasons.rate_limited|{"seconds":2,"reason":"rate_limited","code":"resource_exhausted"}',
    );
    expect(d.detail).toBe('slow down');
    expect(d.reason).toBe('rate_limited');
  });

  it('falls back to the code for unknown reasons', () => {
    const d = describeError(apiError(Code.NotFound, 'site x not found', 'something_new'), t);
    expect(d.title.startsWith('errors.codes.not_found')).toBe(true);
    expect(d.reason).toBe('something_new');
  });

  it('builds one-line messages', () => {
    const tt = (key: string) => key;
    expect(errorMessage(apiError(Code.PermissionDenied, 'no access', 'permission_denied'), tt)).toBe(
      'errors.reasons.permission_denied: no access',
    );
    expect(errorMessage(apiError(Code.Internal, ''), tt)).toBe('errors.codes.internal');
  });

  it('knows every reason of spec section 10', () => {
    expect(KNOWN_REASONS).toHaveLength(29);
    expect(isKnownReason('circuit_open')).toBe(true);
    expect(isKnownReason('nope')).toBe(false);
    expect(isKnownReason(undefined)).toBe(false);
  });
});

describe('shouldRetryQuery', () => {
  it('never retries permission, auth and argument errors', () => {
    for (const code of [Code.PermissionDenied, Code.Unauthenticated, Code.InvalidArgument, Code.NotFound]) {
      expect(shouldRetryQuery(0, apiError(code, 'x'))).toBe(false);
    }
    expect(isPermissionDenied(apiError(Code.PermissionDenied, 'x'))).toBe(true);
  });

  it('retries transient errors a limited number of times', () => {
    const err = apiError(Code.Unavailable, 'down');
    expect(shouldRetryQuery(0, err)).toBe(true);
    expect(shouldRetryQuery(1, err)).toBe(true);
    expect(shouldRetryQuery(2, err)).toBe(false);
  });
});
