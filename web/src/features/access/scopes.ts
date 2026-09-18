/**
 * API token scopes (implementation spec section 3.2, "Token scopes"). A scope
 * string is a scope name optionally followed by ":<argument>", e.g.
 * "lease:acquire", "lease:acquire:shop", "config:read:crawler*",
 * "secret:read:prod/signing/*" or "admin".
 */

/** Scope names a token can be granted. */
export const TOKEN_SCOPE_NAMES = [
  'lease:acquire',
  'report:write',
  'config:read',
  'config:publish',
  'secret:read',
  'identity:write',
  'proxy:write',
  'admin',
] as const;

export type TokenScopeName = (typeof TOKEN_SCOPE_NAMES)[number];

/**
 * Argument kind of a scope: a site name, a config group glob, a secret path
 * glob over "<namespace>/<path>", or none.
 */
export type ScopeArgKind = 'site' | 'group' | 'path' | 'none';

export interface ScopeDefinition {
  name: TokenScopeName;
  arg: ScopeArgKind;
  /** True when the argument is mandatory (secret:read). */
  argRequired: boolean;
}

export const SCOPE_DEFINITIONS: Readonly<Record<TokenScopeName, ScopeDefinition>> = {
  'lease:acquire': { name: 'lease:acquire', arg: 'site', argRequired: false },
  'report:write': { name: 'report:write', arg: 'site', argRequired: false },
  'config:read': { name: 'config:read', arg: 'group', argRequired: false },
  'config:publish': { name: 'config:publish', arg: 'group', argRequired: false },
  'secret:read': { name: 'secret:read', arg: 'path', argRequired: true },
  'identity:write': { name: 'identity:write', arg: 'site', argRequired: false },
  'proxy:write': { name: 'proxy:write', arg: 'none', argRequired: false },
  admin: { name: 'admin', arg: 'none', argRequired: false },
};

/** CreateTokenRequest.scopes limits. */
export const MAX_SCOPES = 64;
export const MAX_SCOPE_LENGTH = 256;

/**
 * Glob over config group names: group characters (letters, digits, "_", ".",
 * "-") plus the wildcards "*" and "?" (the server glob has no character classes).
 */
const GROUP_GLOB_PATTERN = /^[A-Za-z0-9_.*?-]+$/;
/** Glob over "<namespace>/<path>" secret paths. */
const PATH_GLOB_PATTERN = /^[A-Za-z0-9_.*?/-]+$/;
/**
 * Site names are compared exactly, so the argument must be a site name
 * (SiteAdminService pattern) and never a wildcard: the server rejects "*" and "?".
 */
const SITE_NAME_PATTERN = /^[a-z0-9_][a-z0-9_.-]{0,63}$/;

export function isTokenScopeName(name: string): name is TokenScopeName {
  return (TOKEN_SCOPE_NAMES as readonly string[]).includes(name);
}

/** One row of the scope builder. */
export interface ScopeRow {
  /** Stable key for rendering. */
  id: string;
  name: TokenScopeName;
  /** Argument; empty means "every site / group" (or none). */
  arg: string;
}

let rowSequence = 0;

/** Returns a unique scope row ID. */
export function newScopeRowId(): string {
  rowSequence += 1;
  return `scope-${rowSequence}`;
}

/** Creates a builder row. */
export function createScopeRow(name: TokenScopeName, arg = ''): ScopeRow {
  return { id: newScopeRowId(), name, arg };
}

/** Builds the scope string of a name and argument ("name" or "name:arg"). */
export function buildScope(name: TokenScopeName, arg: string): string {
  const definition = SCOPE_DEFINITIONS[name];
  const trimmed = arg.trim();
  if (definition.arg === 'none' || trimmed === '') return name;
  return `${name}:${trimmed}`;
}

/** A scope string split into name and argument. */
export interface ParsedScope {
  /** Scope string as given. */
  raw: string;
  /** Known scope name, or the raw string for unknown scopes. */
  name: string;
  arg: string;
  known: boolean;
  definition?: ScopeDefinition;
}

/**
 * Parses a scope string back into name and argument. Scope names contain
 * colons themselves, so the longest known name prefix wins.
 */
export function parseScope(raw: string): ParsedScope {
  const scope = raw.trim();
  let match: TokenScopeName | undefined;
  for (const name of TOKEN_SCOPE_NAMES) {
    if (scope === name || scope.startsWith(`${name}:`)) {
      if (!match || name.length > match.length) match = name;
    }
  }
  if (!match) return { raw, name: scope, arg: '', known: false };
  const definition = SCOPE_DEFINITIONS[match];
  const arg = scope.length > match.length ? scope.slice(match.length + 1) : '';
  // An argument on a scope that takes none is not something the builder produces.
  if (definition.arg === 'none' && arg !== '') return { raw, name: scope, arg: '', known: false };
  return { raw, name: match, arg, known: true, definition };
}

/** Scope strings of the rows, in order and without duplicates. */
export function scopesFromRows(rows: readonly ScopeRow[]): string[] {
  const seen = new Set<string>();
  const scopes: string[] = [];
  for (const row of rows) {
    const scope = buildScope(row.name, row.arg);
    if (seen.has(scope)) continue;
    seen.add(scope);
    scopes.push(scope);
  }
  return scopes;
}

/** Builder rows for scope strings; unknown scopes are skipped. */
export function rowsFromScopes(scopes: readonly string[]): ScopeRow[] {
  return scopes
    .map(parseScope)
    .filter((parsed): parsed is ParsedScope & { name: TokenScopeName } => parsed.known)
    .map((parsed) => createScopeRow(parsed.name, parsed.arg));
}

export type ScopeRowError = 'argRequired' | 'argInvalid' | 'duplicate' | 'tooLong';

/** Validates one argument for a scope name; undefined when valid. */
export function validateScopeArg(name: TokenScopeName, arg: string): ScopeRowError | undefined {
  const definition = SCOPE_DEFINITIONS[name];
  const value = arg.trim();
  if (definition.arg === 'none') return undefined;
  if (value === '') return definition.argRequired ? 'argRequired' : undefined;
  if (buildScope(name, value).length > MAX_SCOPE_LENGTH) return 'tooLong';
  const pattern =
    definition.arg === 'site'
      ? SITE_NAME_PATTERN
      : definition.arg === 'group'
        ? GROUP_GLOB_PATTERN
        : PATH_GLOB_PATTERN;
  if (!pattern.test(value)) return 'argInvalid';
  if (definition.arg === 'path' && (!value.includes('/') || value.startsWith('/'))) return 'argInvalid';
  return undefined;
}

/** Per-row errors keyed by row ID (rows without errors are absent). */
export function validateScopeRows(rows: readonly ScopeRow[]): Record<string, ScopeRowError> {
  const errors: Record<string, ScopeRowError> = {};
  const seen = new Set<string>();
  for (const row of rows) {
    const argError = validateScopeArg(row.name, row.arg);
    if (argError) {
      errors[row.id] = argError;
      continue;
    }
    const scope = buildScope(row.name, row.arg);
    if (seen.has(scope)) errors[row.id] = 'duplicate';
    seen.add(scope);
  }
  return errors;
}

/** Scope presets offered by the builder. */
export const SCOPE_PRESETS = [
  { id: 'crawlerNode', scopes: ['lease:acquire', 'report:write', 'config:read'] },
  { id: 'cookieRefresher', scopes: ['identity:write'] },
  { id: 'ciPublisher', scopes: ['config:publish'] },
] as const satisfies readonly { id: string; scopes: readonly TokenScopeName[] }[];

export type ScopePresetId = (typeof SCOPE_PRESETS)[number]['id'];

/** Scope strings of a preset. */
export function expandPreset(id: ScopePresetId): string[] {
  const preset = SCOPE_PRESETS.find((p) => p.id === id);
  return preset ? [...preset.scopes] : [];
}

/** Appends the preset's scopes that the rows do not contain yet. */
export function applyPreset(rows: readonly ScopeRow[], id: ScopePresetId): ScopeRow[] {
  const existing = new Set(scopesFromRows(rows));
  const additions = rowsFromScopes(expandPreset(id)).filter(
    (row) => !existing.has(buildScope(row.name, row.arg)),
  );
  return [...rows, ...additions];
}
