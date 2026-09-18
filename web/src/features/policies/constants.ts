/**
 * Enumerations of the policy specification (implementation spec section 7,
 * internal/policy). Values are the wire strings used in YAML and the API.
 */

export const POLICY_KINDS = ['rotation', 'signal', 'action', 'breaker'] as const;
export type PolicyKind = (typeof POLICY_KINDS)[number];

export function isPolicyKind(value: unknown): value is PolicyKind {
  return typeof value === 'string' && (POLICY_KINDS as readonly string[]).includes(value);
}

export const OUTCOMES = [
  'success',
  'empty',
  'rate_limited',
  'captcha',
  'auth_invalid',
  'forbidden',
  'banned',
  'proxy_error',
  'network_error',
  'target_error',
  'client_error',
  'unknown',
] as const;
export type Outcome = (typeof OUTCOMES)[number];

export const BLAMES = ['none', 'identity', 'proxy', 'both'] as const;

/** Node-side transport error kinds accepted in reports and signal rules ("other" = unclassified). */
export const ERROR_KINDS = [
  'timeout',
  'conn_reset',
  'conn_refused',
  'proxy_auth',
  'tls',
  'dns',
  'other',
] as const;

export const ACTIONS = ['cooldown', 'ban', 'expire', 'quarantine'] as const;
export type ActionKind = (typeof ACTIONS)[number];

export const SCOPES = [
  'identity_endpoint',
  'identity_site',
  'identity',
  'account',
  'proxy_site',
  'proxy',
] as const;
export type ActionScope = (typeof SCOPES)[number];

/** Scopes offered per action (spec section 7; "identity" for cooldown is the same as identity_site). */
export const SCOPES_BY_ACTION: Readonly<Record<ActionKind, readonly ActionScope[]>> = {
  cooldown: ['identity_endpoint', 'identity_site', 'account', 'proxy_site', 'proxy'],
  ban: ['identity', 'account', 'proxy'],
  expire: ['identity'],
  quarantine: ['identity', 'proxy'],
};

export function isActionKind(value: string): value is ActionKind {
  return (ACTIONS as readonly string[]).includes(value);
}

/** Scopes compatible with an action; unknown actions allow no scope. */
export function scopesForAction(action: string): readonly ActionScope[] {
  return isActionKind(action) ? SCOPES_BY_ACTION[action] : [];
}

/** Reports whether the action supports the scope. */
export function isScopeAllowed(action: string, scope: string): boolean {
  return scopesForAction(action).some((s) => s === scope);
}

/**
 * Keeps the scope when the action supports it, otherwise returns the first
 * compatible scope (used when the action of a rule changes).
 */
export function coerceScope(action: string, scope: string): string {
  if (action === 'cooldown' && scope === 'identity') return 'identity_site';
  if (isScopeAllowed(action, scope)) return scope;
  return scopesForAction(action)[0] ?? scope;
}

export const ACTION_MODES = ['enforce', 'shadow'] as const;
export const BAN_EXPIRY_STATES = ['pending', 'active'] as const;

export const ROTATION_STRATEGIES = [
  'weighted_random',
  'least_recently_used',
  'round_robin',
  'best_health',
] as const;
export const REUSE_ANCHORS = ['released', 'acquired'] as const;
export const REUSE_SCOPES = ['endpoint_group', 'site'] as const;
export const PROXY_MODES = ['none', 'pool', 'bind_identity', 'region_match'] as const;
export const PROXY_KINDS = ['datacenter', 'residential', 'mobile', 'tunnel'] as const;

export const REVERT_MODES = ['none', 'endpoint', 'all'] as const;
export type RevertMode = (typeof REVERT_MODES)[number];

export const BINDING_LEVELS = ['namespace', 'site', 'client', 'endpoint_group'] as const;

export const IDENTITY_STATES = [
  'active',
  'pending',
  'expired',
  'banned',
  'quarantined',
  'disabled',
  'retired',
] as const;

export const COUNTER_SUBJECTS = ['identity', 'account', 'proxy'] as const;
export type CounterSubject = (typeof COUNTER_SUBJECTS)[number];

/** Rule names: ^[a-z0-9][a-z0-9._-]{0,63}$ (also policy names). */
export const NAME_PATTERN = /^[a-z0-9][a-z0-9._-]{0,63}$/;

/** Maximum policy YAML size accepted by the API. */
export const MAX_YAML_BYTES = 1024 * 1024;
