import { create } from '@bufbuild/protobuf';
import { describe, expect, it } from 'vitest';

import { URIRuleSchema } from '@/gen/spinneret/v1/site_admin_pb';

import {
  draftsFromRules,
  hasBlockingErrors,
  moveRule,
  parseServerRuleIndex,
  re2ToJs,
  removeRule,
  rulesChanged,
  toRuleInputs,
  updateRule,
  validateRuleList,
  validateRulePattern,
  type RuleDraft,
  type URIRuleKind,
} from './uriRules';

const draft = (key: string, kind: URIRuleKind, pattern: string): RuleDraft => ({
  key,
  id: key,
  kind,
  pattern,
});
const codes = (kind: URIRuleKind, pattern: string) =>
  validateRulePattern(kind, pattern).map((h) => `${h.level}:${h.code}`);

describe('rule ordering', () => {
  const list = [draft('a', 'prefix', '/a/'), draft('b', 'regex', '^/b'), draft('c', 'exact', '/c')];

  it('moves rows without mutating the input', () => {
    const moved = moveRule(list, 0, 2);
    expect(moved.map((r) => r.key)).toEqual(['b', 'c', 'a']);
    expect(list.map((r) => r.key)).toEqual(['a', 'b', 'c']);
    expect(moveRule(list, 2, 0).map((r) => r.key)).toEqual(['c', 'a', 'b']);
    expect(moveRule(list, 1, 2).map((r) => r.key)).toEqual(['a', 'c', 'b']);
  });

  it('ignores out-of-range and no-op moves', () => {
    expect(moveRule(list, 0, 0)).toBe(list);
    expect(moveRule(list, -1, 1)).toBe(list);
    expect(moveRule(list, 1, 3)).toBe(list);
  });

  it('assigns positions from list order', () => {
    expect(toRuleInputs(moveRule(list, 2, 0))).toEqual([
      { id: 'c', kind: 'exact', pattern: '/c', position: 0 },
      { id: 'a', kind: 'prefix', pattern: '/a/', position: 1 },
      { id: 'b', kind: 'regex', pattern: '^/b', position: 2 },
    ]);
  });

  it('orders stored rules by position and keeps IDs as keys', () => {
    const rules = [
      create(URIRuleSchema, { id: 'uri_2', kind: 'regex', pattern: '^/x', position: 5 }),
      create(URIRuleSchema, { id: 'uri_1', kind: 'prefix', pattern: '/y/', position: 1 }),
      create(URIRuleSchema, { id: '', kind: 'bogus', pattern: '/z', position: 1 }),
    ];
    const drafts = draftsFromRules(rules);
    expect(drafts.map((d) => [d.id, d.kind, d.pattern])).toEqual([
      ['uri_1', 'prefix', '/y/'],
      ['', 'exact', '/z'],
      ['uri_2', 'regex', '^/x'],
    ]);
    expect(drafts[0]?.key).toBe('uri_1');
    expect(drafts[1]?.key).toMatch(/^rule-/);
  });

  it('detects changes in order, kind and pattern', () => {
    expect(rulesChanged(list, [...list])).toBe(false);
    expect(rulesChanged(list, moveRule(list, 0, 1))).toBe(true);
    expect(rulesChanged(list, updateRule(list, 'a', { kind: 'exact' }))).toBe(true);
    expect(rulesChanged(list, removeRule(list, 'c'))).toBe(true);
    expect(updateRule(list, 'a', { key: 'zzz', pattern: '/q' })[0]).toEqual({
      key: 'a',
      id: 'a',
      kind: 'prefix',
      pattern: '/q',
    });
  });
});

describe('pattern hints', () => {
  it('requires a leading slash and rejects query, fragment and whitespace for path kinds', () => {
    expect(codes('exact', 'api/x')).toEqual(['error:must_start_with_slash']);
    expect(codes('prefix', '/api?x=1')).toEqual(['error:no_query_fragment']);
    expect(codes('template', '/a b/{id}')).toEqual(['error:whitespace']);
    expect(codes('exact', '')).toEqual(['error:required']);
    expect(codes('exact', `/${'a'.repeat(1024)}`)).toEqual(['error:too_long']);
  });

  it('validates template segments like the backend', () => {
    expect(codes('template', '/api/item/{id}/detail')).toEqual([]);
    expect(codes('template', '/api/item-{id}')).toEqual(['error:template_segment']);
    expect(codes('template', '/api/{1id}')).toEqual(['error:template_param_name']);
    expect(codes('template', '/api/{}')).toEqual(['error:template_param_name']);
    expect(codes('template', '/{id}/x/{id}')).toEqual(['error:template_duplicate_param']);
    expect(codes('template', '/api/list')).toEqual(['info:template_no_params']);
    expect(validateRulePattern('template', '/a/{x}{y}')[0]?.params).toEqual({ segment: 2 });
  });

  it('warns about literal braces and wildcards and hints at prefix boundaries', () => {
    expect(codes('exact', '/api/{id}')).toEqual(['warning:braces_literal']);
    expect(codes('prefix', '/api/*')).toEqual(['warning:wildcard_literal']);
    expect(codes('prefix', '/api/item')).toEqual(['info:prefix_no_trailing_slash']);
    expect(codes('prefix', '/api/')).toEqual([]);
    expect(codes('prefix', '/')).toEqual([]);
  });

  it('checks regex patterns for RE2 compatibility', () => {
    expect(codes('regex', '^/api/v[0-9]+/item$')).toEqual([]);
    expect(codes('regex', '/api/')).toEqual(['info:regex_unanchored']);
    expect(codes('regex', '^/(?=a)')).toEqual(['error:regex_unsupported']);
    expect(codes('regex', '^/(a)\\1')).toEqual(['error:regex_unsupported']);
    expect(codes('regex', '^/\\(?=x')).toEqual([]);
    expect(codes('regex', '^/[(?=]')).toEqual([]);
    expect(codes('regex', '^/(unclosed')).toEqual(['warning:regex_invalid']);
    expect(codes('regex', '(?i)^/API/(?P<id>\\d+)\\z')).toEqual([]);
    expect(re2ToJs('(?i)^/(?P<id>x)(?s:.)\\z')).toBe('^/(?<id>x)(?:.)$');
  });

  it('flags duplicate kind and pattern pairs and blocking errors', () => {
    const hints = validateRuleList([
      draft('a', 'prefix', '/a/'),
      draft('b', 'exact', '/a/'),
      draft('c', 'prefix', '/a/'),
    ]);
    expect(hints.get('a')).toEqual([]);
    expect(hints.get('b')).toEqual([]);
    expect(hints.get('c')?.[0]).toEqual({ level: 'error', code: 'duplicate' });
    expect(hasBlockingErrors(hints)).toBe(true);
    expect(hasBlockingErrors(validateRuleList([draft('x', 'regex', '/x')]))).toBe(false);
  });

  it('maps server validation messages to rows', () => {
    expect(parseServerRuleIndex('rules[3]: pattern must start with "/"')).toBe(3);
    expect(parseServerRuleIndex('at most 1000 uri rules')).toBeUndefined();
  });
});
