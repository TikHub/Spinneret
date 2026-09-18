import { type URIRule } from '@/gen/spinneret/v1/site_admin_pb';

/** URI rule match kinds (match priority: exact > template > prefix > regex > _default). */
export const URI_RULE_KINDS = ['exact', 'template', 'prefix', 'regex'] as const;
export type URIRuleKind = (typeof URI_RULE_KINDS)[number];

/** Pattern length limit (URIRule.pattern max_len). */
export const MAX_PATTERN_LENGTH = 1024;
/** Rules per ReplaceURIRules request. */
export const MAX_RULES = 500;
/** Name of the fallback endpoint group, which cannot have rules. */
export const DEFAULT_GROUP_NAME = '_default';

/** Editable rule row; `key` is stable across reorders (React key, drag identity). */
export interface RuleDraft {
  key: string;
  /** Stored rule ID; empty for new rows. */
  id: string;
  kind: URIRuleKind;
  pattern: string;
}

let draftSequence = 0;

/** Returns a new unique row key. */
export function newRuleKey(): string {
  draftSequence += 1;
  return `rule-${draftSequence}`;
}

export function isRuleKind(value: string): value is URIRuleKind {
  return (URI_RULE_KINDS as readonly string[]).includes(value);
}

/** Converts stored rules (any order) to drafts ordered by position; unknown kinds fall back to exact. */
export function draftsFromRules(rules: readonly URIRule[]): RuleDraft[] {
  return rules
    .map((rule, index) => ({ rule, index }))
    .sort((a, b) => a.rule.position - b.rule.position || a.index - b.index)
    .map(({ rule }) => ({
      key: rule.id || newRuleKey(),
      id: rule.id,
      kind: isRuleKind(rule.kind) ? rule.kind : 'exact',
      pattern: rule.pattern,
    }));
}

/** Creates an empty row; prefix rules are the most common starting point. */
export function newRuleDraft(kind: URIRuleKind = 'prefix'): RuleDraft {
  return { key: newRuleKey(), id: '', kind, pattern: '/' };
}

/** Moves the row at `from` to index `to` (immutable); out-of-range moves return the same list. */
export function moveRule<T>(list: readonly T[], from: number, to: number): readonly T[] {
  if (from === to || from < 0 || to < 0 || from >= list.length || to >= list.length) return list;
  const next = [...list];
  const [item] = next.splice(from, 1);
  if (item === undefined) return list;
  next.splice(to, 0, item);
  return next;
}

/** Replaces the row with the given key (immutable). */
export function updateRule(list: readonly RuleDraft[], key: string, patch: Partial<RuleDraft>): RuleDraft[] {
  return list.map((rule) => (rule.key === key ? { ...rule, ...patch, key: rule.key } : rule));
}

/** Removes the row with the given key (immutable). */
export function removeRule(list: readonly RuleDraft[], key: string): RuleDraft[] {
  return list.filter((rule) => rule.key !== key);
}

/** Plain init objects of URIRule for ReplaceURIRules; positions follow the list order. */
export function toRuleInputs(
  list: readonly RuleDraft[],
): Array<{ id: string; kind: string; pattern: string; position: number }> {
  return list.map((rule, position) => ({ id: rule.id, kind: rule.kind, pattern: rule.pattern, position }));
}

/** True when kinds, patterns or their order differ. */
export function rulesChanged(a: readonly RuleDraft[], b: readonly RuleDraft[]): boolean {
  return (
    a.length !== b.length || a.some((rule, i) => rule.kind !== b[i]?.kind || rule.pattern !== b[i]?.pattern)
  );
}

export type HintLevel = 'error' | 'warning' | 'info';

export type RuleHintCode =
  | 'required'
  | 'too_long'
  | 'must_start_with_slash'
  | 'no_query_fragment'
  | 'whitespace'
  | 'braces_literal'
  | 'wildcard_literal'
  | 'prefix_no_trailing_slash'
  | 'template_segment'
  | 'template_param_name'
  | 'template_duplicate_param'
  | 'template_no_params'
  | 'regex_unsupported'
  | 'regex_invalid'
  | 'regex_unanchored'
  | 'duplicate';

export interface RuleHint {
  level: HintLevel;
  code: RuleHintCode;
  params?: Record<string, string | number>;
}

const PARAM_NAME = /^[a-zA-Z_][a-zA-Z0-9_]*$/;
// eslint-disable-next-line no-control-regex
const WHITESPACE_OR_CONTROL = /[\s\u0000-\u001f\u007f-\u009f]/;

function validatePathShape(pattern: string): RuleHint[] {
  const hints: RuleHint[] = [];
  if (!pattern.startsWith('/')) hints.push({ level: 'error', code: 'must_start_with_slash' });
  if (/[?#]/.test(pattern)) hints.push({ level: 'error', code: 'no_query_fragment' });
  if (WHITESPACE_OR_CONTROL.test(pattern)) hints.push({ level: 'error', code: 'whitespace' });
  return hints;
}

function validateTemplate(pattern: string): RuleHint[] {
  const hints: RuleHint[] = [];
  const names = new Set<string>();
  const segments = pattern.startsWith('/') ? pattern.slice(1).split('/') : pattern.split('/');
  segments.forEach((segment, index) => {
    if (!/[{}]/.test(segment)) return;
    const whole = /^\{([^{}]*)\}$/.exec(segment);
    if (!whole) {
      hints.push({ level: 'error', code: 'template_segment', params: { segment: index + 1 } });
      return;
    }
    const name = whole[1] ?? '';
    if (!PARAM_NAME.test(name)) {
      hints.push({ level: 'error', code: 'template_param_name', params: { segment: index + 1 } });
    } else if (names.has(name)) {
      hints.push({ level: 'error', code: 'template_duplicate_param', params: { name } });
    } else {
      names.add(name);
    }
  });
  if (names.size === 0 && !hints.some((h) => h.level === 'error')) {
    hints.push({ level: 'info', code: 'template_no_params' });
  }
  return hints;
}

interface RegexScan {
  lookaround: boolean;
  backreference: boolean;
}

/** Scans a regex for constructs RE2 rejects (lookaround, backreferences), skipping escapes and classes. */
function scanRegex(pattern: string): RegexScan {
  const scan: RegexScan = { lookaround: false, backreference: false };
  let inClass = false;
  for (let i = 0; i < pattern.length; i += 1) {
    const c = pattern[i];
    if (c === '\\') {
      const next = pattern[i + 1] ?? '';
      if (!inClass && (/[1-9]/.test(next) || (next === 'k' && pattern[i + 2] === '<'))) {
        scan.backreference = true;
      }
      i += 1;
    } else if (inClass) {
      if (c === ']') inClass = false;
    } else if (c === '[') {
      inClass = true;
      if (pattern[i + 1] === ']') i += 1;
    } else if (c === '(' && pattern[i + 1] === '?') {
      const rest = pattern.slice(i + 2, i + 4);
      if (rest.startsWith('=') || rest.startsWith('!') || rest === '<=' || rest === '<!')
        scan.lookaround = true;
    }
  }
  return scan;
}

/** Rewrites RE2-only syntax to a JavaScript equivalent so the browser can check the structure. */
export function re2ToJs(pattern: string): string {
  return pattern
    .replace(/\(\?P</g, '(?<')
    .replace(/\(\?[imsU]+\)/g, '')
    .replace(/\(\?[imsU-]+:/g, '(?:')
    .replace(/\\A/g, '^')
    .replace(/\\z/g, '$');
}

function validateRegex(pattern: string): RuleHint[] {
  const hints: RuleHint[] = [];
  const scan = scanRegex(pattern);
  if (scan.lookaround || scan.backreference) {
    hints.push({ level: 'error', code: 'regex_unsupported' });
    return hints;
  }
  const jsPattern = re2ToJs(pattern);
  try {
    new RegExp(jsPattern);
  } catch (err) {
    hints.push({
      level: 'warning',
      code: 'regex_invalid',
      params: { message: err instanceof Error ? err.message : String(err) },
    });
  }
  if (!jsPattern.startsWith('^')) hints.push({ level: 'info', code: 'regex_unanchored' });
  return hints;
}

/**
 * Client-side hints for a rule pattern, mirroring internal/site ValidatePattern.
 * Errors block saving; warnings and infos only inform (the server is authoritative,
 * e.g. RE2 syntax is only approximated by the browser's RegExp).
 */
export function validateRulePattern(kind: URIRuleKind, pattern: string): RuleHint[] {
  if (pattern === '') return [{ level: 'error', code: 'required' }];
  if ([...pattern].length > MAX_PATTERN_LENGTH) {
    return [{ level: 'error', code: 'too_long', params: { max: MAX_PATTERN_LENGTH } }];
  }
  if (kind === 'regex') return validateRegex(pattern);

  const hints = validatePathShape(pattern);
  if (kind === 'template') {
    hints.push(...validateTemplate(pattern));
  } else if (/[{}]/.test(pattern)) {
    hints.push({ level: 'warning', code: 'braces_literal' });
  }
  if (pattern.includes('*')) hints.push({ level: 'warning', code: 'wildcard_literal' });
  if (kind === 'prefix' && pattern.length > 1 && !pattern.endsWith('/') && hints.length === 0) {
    hints.push({ level: 'info', code: 'prefix_no_trailing_slash' });
  }
  return hints;
}

/** Hints per row key, including duplicate (kind, pattern) pairs. */
export function validateRuleList(list: readonly RuleDraft[]): Map<string, RuleHint[]> {
  const result = new Map<string, RuleHint[]>();
  const seen = new Set<string>();
  for (const rule of list) {
    const hints = validateRulePattern(rule.kind, rule.pattern);
    const identity = `${rule.kind}\u0000${rule.pattern}`;
    if (seen.has(identity)) hints.unshift({ level: 'error', code: 'duplicate' });
    seen.add(identity);
    result.set(rule.key, hints);
  }
  return result;
}

export function hasBlockingErrors(hints: ReadonlyMap<string, readonly RuleHint[]>): boolean {
  return [...hints.values()].some((list) => list.some((hint) => hint.level === 'error'));
}

/** Extracts the row index from a server validation message such as "rules[3]: pattern must start with /". */
export function parseServerRuleIndex(message: string): number | undefined {
  const match = /rules\[(\d+)\]/.exec(message);
  if (!match) return undefined;
  const index = Number(match[1]);
  return Number.isSafeInteger(index) ? index : undefined;
}
