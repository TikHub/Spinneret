import { Code, ConnectError } from '@connectrpc/connect';

/** Response header carrying the machine-readable error reason. */
export const REASON_HEADER = 'Spinneret-Reason';
/** Response header carrying a retry hint in milliseconds. */
export const RETRY_AFTER_HEADER = 'Spinneret-Retry-After-Ms';

/** Error reasons of implementation spec section 10 (mirrors internal/apperr). */
export const KNOWN_REASONS = [
  'token_invalid',
  'token_expired',
  'token_revoked',
  'ip_not_allowed',
  'session_invalid',
  'csrf_missing',
  'login_throttled',
  'scope_missing',
  'permission_denied',
  'site_unknown',
  'client_unknown',
  'endpoint_group_unknown',
  'uri_invalid',
  'invalid_argument',
  'no_identity_available',
  'no_proxy_available',
  'circuit_open',
  'site_paused',
  'rebuilding',
  'lease_unknown',
  'lease_released',
  'lease_expired',
  'lease_lifetime_exceeded',
  'rate_limited',
  'not_found',
  'already_exists',
  'failed_precondition',
  'conflict',
  'internal',
] as const;

export type KnownReason = (typeof KNOWN_REASONS)[number];

const knownReasonSet: ReadonlySet<string> = new Set(KNOWN_REASONS);

/** Normalized view of an API error. */
export interface ApiError {
  code: Code;
  /** snake_case Connect code name, e.g. "permission_denied". */
  codeName: string;
  /** Spinneret-Reason header value, when present. */
  reason?: string;
  /** Server message without the "[code]" prefix. */
  message: string;
  /** Spinneret-Retry-After-Ms header value, when present and valid. */
  retryAfterMs?: number;
  cause: ConnectError;
}

/** Minimal translate function (compatible with i18next's t). */
export type Translate = (key: string, options?: Record<string, unknown>) => string;

export function isKnownReason(reason: string | undefined): reason is KnownReason {
  return reason !== undefined && knownReasonSet.has(reason);
}

/** Converts a Connect code to its snake_case name ("PermissionDenied" -> "permission_denied"). */
export function codeName(code: Code): string {
  const name = Code[code] as string | undefined;
  if (!name) return 'unknown';
  return name.replace(/([a-z0-9])([A-Z])/g, '$1_$2').toLowerCase();
}

/** Normalizes any thrown value into an ApiError. */
export function toApiError(err: unknown): ApiError {
  const cause = ConnectError.from(err);
  const reason = cause.metadata.get(REASON_HEADER)?.trim() || undefined;
  const retryRaw = cause.metadata.get(RETRY_AFTER_HEADER);
  const retry = retryRaw === null ? Number.NaN : Number(retryRaw);
  return {
    code: cause.code,
    codeName: codeName(cause.code),
    reason,
    message: cause.rawMessage,
    retryAfterMs: Number.isFinite(retry) && retry >= 0 ? retry : undefined,
    cause,
  };
}

/** Returns the Spinneret-Reason of an error, if any. */
export function errorReason(err: unknown): string | undefined {
  return toApiError(err).reason;
}

/** Returns the retry hint of an error in milliseconds, if any. */
export function retryAfterMs(err: unknown): number | undefined {
  return toApiError(err).retryAfterMs;
}

/** Reports whether the error carries one of the given Connect codes. */
export function hasCode(err: unknown, ...codes: Code[]): boolean {
  if (err === null || err === undefined) return false;
  return codes.includes(ConnectError.from(err).code);
}

export function isUnauthenticated(err: unknown): boolean {
  return hasCode(err, Code.Unauthenticated);
}

export function isPermissionDenied(err: unknown): boolean {
  return hasCode(err, Code.PermissionDenied);
}

export function isNotFound(err: unknown): boolean {
  return hasCode(err, Code.NotFound);
}

/** Codes that never succeed on retry without user action. */
const NON_RETRYABLE_CODES: readonly Code[] = [
  Code.Unauthenticated,
  Code.PermissionDenied,
  Code.InvalidArgument,
  Code.NotFound,
  Code.AlreadyExists,
  Code.FailedPrecondition,
  Code.Unimplemented,
  Code.OutOfRange,
];

/** Maximum automatic query retries for transient errors. */
export const MAX_QUERY_RETRIES = 2;

/** React Query retry predicate: retries transient errors only. */
export function shouldRetryQuery(failureCount: number, err: unknown): boolean {
  if (failureCount >= MAX_QUERY_RETRIES) return false;
  return !hasCode(err, ...NON_RETRYABLE_CODES);
}

/** Human-readable description of an error. */
export interface ErrorDescription {
  /** Translated summary (from the reason when known, otherwise from the code). */
  title: string;
  /** Raw server message; omitted when empty. */
  detail?: string;
  reason?: string;
  code: string;
  retryAfterMs?: number;
}

/**
 * Describes an error for display: known reasons map to `errors.reasons.<reason>`,
 * other errors to `errors.codes.<code>` (namespace common). The raw message is
 * returned separately so pages can show both.
 */
export function describeError(err: unknown, t: Translate): ErrorDescription {
  const api = toApiError(err);
  const seconds =
    api.retryAfterMs === undefined ? undefined : Math.max(1, Math.ceil(api.retryAfterMs / 1000));
  const options = { seconds: seconds ?? 0, reason: api.reason ?? '', code: api.codeName };
  const title = isKnownReason(api.reason)
    ? t(`errors.reasons.${api.reason}`, options)
    : t(`errors.codes.${api.codeName}`, options);
  return {
    title,
    detail: api.message || undefined,
    reason: api.reason,
    code: api.codeName,
    retryAfterMs: api.retryAfterMs,
  };
}

/** One-line message suitable for toasts: "<title>: <detail>" (detail omitted when redundant). */
export function errorMessage(err: unknown, t: Translate): string {
  const d = describeError(err, t);
  if (!d.detail || d.detail === d.title) return d.title;
  return `${d.title}: ${d.detail}`;
}
