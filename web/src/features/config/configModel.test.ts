import { create } from '@bufbuild/protobuf';
import { describe, expect, it } from 'vitest';

import { ConfigItemInfoSchema } from '@/gen/spinneret/v1/config_admin_pb';

import {
  buildConfigTree,
  checkJsonSyntax,
  formatLanguage,
  formatSupportsSchema,
  isReadOnlyItem,
  offsetToPosition,
  validateConfigGroup,
  validateConfigKey,
  validateSchemaDocument,
} from './configModel';

describe('validateConfigGroup', () => {
  it('accepts names matching the create pattern', () => {
    for (const group of ['crawler', 'a', 'A.b-c_d', '0group', 'x'.repeat(64)]) {
      expect(validateConfigGroup(group)).toBeUndefined();
    }
  });

  it('rejects empty, reserved and malformed names', () => {
    expect(validateConfigGroup('')).toBe('required');
    expect(validateConfigGroup('_runtime')).toBe('reserved');
    expect(validateConfigGroup('-lead')).toBe('pattern');
    expect(validateConfigGroup('.lead')).toBe('pattern');
    expect(validateConfigGroup('has space')).toBe('pattern');
    expect(validateConfigGroup('with/slash')).toBe('pattern');
    expect(validateConfigGroup('x'.repeat(65))).toBe('pattern');
  });
});

describe('validateConfigKey', () => {
  it('accepts keys with slashes and dots', () => {
    for (const key of ['search.json', 'a/b/c.yaml', 'K-1_2', 'k'.repeat(128)]) {
      expect(validateConfigKey(key)).toBeUndefined();
    }
  });

  it('rejects empty and malformed keys', () => {
    expect(validateConfigKey('')).toBe('required');
    expect(validateConfigKey('/lead')).toBe('pattern');
    expect(validateConfigKey('_lead')).toBe('pattern');
    expect(validateConfigKey('a b')).toBe('pattern');
    expect(validateConfigKey('k'.repeat(129))).toBe('pattern');
  });
});

describe('formats', () => {
  it('maps formats to editor languages', () => {
    expect(formatLanguage('json')).toBe('json');
    expect(formatLanguage('yaml')).toBe('yaml');
    expect(formatLanguage('text')).toBe('plaintext');
    expect(formatLanguage('unknown')).toBe('plaintext');
  });

  it('allows schemas for json and yaml only', () => {
    expect(formatSupportsSchema('json')).toBe(true);
    expect(formatSupportsSchema('yaml')).toBe(true);
    expect(formatSupportsSchema('text')).toBe(false);
  });
});

describe('isReadOnlyItem', () => {
  it('treats system groups and virtual items as read-only', () => {
    expect(isReadOnlyItem({ group: '_runtime', id: 'cfg_1' })).toBe(true);
    expect(isReadOnlyItem({ group: 'crawler', id: '' })).toBe(true);
    expect(isReadOnlyItem({ group: 'crawler', id: 'cfg_1' })).toBe(false);
  });
});

describe('checkJsonSyntax', () => {
  it('accepts valid JSON', () => {
    expect(checkJsonSyntax('{"a": [1, 2, "${secret:x/y}"]}')).toEqual({
      ok: true,
      value: { a: [1, 2, '${secret:x/y}'] },
    });
  });

  it('reports blank content', () => {
    const result = checkJsonSyntax('  \n');
    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.empty).toBe(true);
  });

  it('reports a marker for syntax errors', () => {
    const result = checkJsonSyntax('{\n  "a": 1,\n  "b": \n}');
    expect(result.ok).toBe(false);
    if (!result.ok) {
      expect(result.empty).toBe(false);
      expect(result.marker.severity).toBe('error');
      expect(result.marker.line).toBeGreaterThanOrEqual(1);
      expect(result.marker.message).not.toBe('');
    }
  });

  it('converts offsets to 1-based positions', () => {
    expect(offsetToPosition('ab\ncd', 0)).toEqual({ line: 1, column: 1 });
    expect(offsetToPosition('ab\ncd', 4)).toEqual({ line: 2, column: 2 });
    expect(offsetToPosition('ab', 99)).toEqual({ line: 1, column: 3 });
  });
});

describe('validateSchemaDocument', () => {
  it('accepts empty, object and boolean schemas', () => {
    expect(validateSchemaDocument('')).toBeUndefined();
    expect(validateSchemaDocument('{"type":"object"}')).toBeUndefined();
    expect(validateSchemaDocument('true')).toBeUndefined();
  });

  it('rejects invalid JSON and non-object documents', () => {
    expect(validateSchemaDocument('{')).toBe('syntax');
    expect(validateSchemaDocument('[1]')).toBe('type');
    expect(validateSchemaDocument('"x"')).toBe('type');
  });
});

describe('buildConfigTree', () => {
  it('groups items with system groups last and sorted keys', () => {
    const item = (group: string, key: string) => create(ConfigItemInfoSchema, { group, key, id: key });
    const tree = buildConfigTree([
      item('_runtime', 'breakers'),
      item('crawler', 'z.json'),
      item('api', 'a.yaml'),
      item('crawler', 'a.json'),
    ]);
    expect(tree.map((g) => g.group)).toEqual(['api', 'crawler', '_runtime']);
    expect(tree[1]?.items.map((i) => i.key)).toEqual(['a.json', 'z.json']);
    expect(tree[2]?.system).toBe(true);
  });
});
