import { describe, expect, it } from 'vitest';

import { blockMap, blockSeq, emitYaml, flowMap, formatString, isPlainSafe, scalar, scalarSeq } from './emit';
import { parseYaml, stripComment, YamlSubsetError } from './parse';

describe('YAML emitter', () => {
  it('quotes strings only when needed', () => {
    expect(formatString('rate_limited')).toBe('rate_limited');
    expect(formatString('/api/item/')).toBe('/api/item/');
    expect(formatString('Built-in default rotation policy.')).toBe('Built-in default rotation policy.');
    expect(formatString('10001')).toBe("'10001'");
    expect(formatString('true')).toBe("'true'");
    expect(formatString('')).toBe("''");
    expect(formatString('a: b')).toBe("'a: b'");
    expect(formatString("it's")).toBe("'it''s'");
    expect(formatString('^/api/v\\d+/')).toBe("'^/api/v\\d+/'");
    expect(formatString('line1\nline2')).toBe('"line1\\nline2"');
    expect(formatString('[x]')).toBe("'[x]'");
    expect(isPlainSafe('-x')).toBe(false);
    expect(isPlainSafe('30m')).toBe(true);
  });

  it('quotes strings the server would read as numbers or timestamps', () => {
    expect(formatString('0010')).toBe("'0010'");
    expect(formatString('1_000')).toBe("'1_000'");
    expect(formatString('0x1F')).toBe("'0x1F'");
    expect(formatString('0b101')).toBe("'0b101'");
    expect(formatString('2024-01-02')).toBe("'2024-01-02'");
    expect(formatString('1.5')).toBe("'1.5'");
    expect(formatString('1.5h')).toBe('1.5h');
    expect(formatString('E42')).toBe('E42');
  });

  it('emits block and flow collections', () => {
    const doc = blockMap([
      ['name', scalar('p')],
      ['skipped', undefined],
      ['list', scalarSeq(['a', 'b'])],
      ['empty', blockSeq([])],
      [
        'rules',
        blockSeq([
          blockMap([
            ['name', scalar('r1')],
            [
              'when',
              flowMap([
                ['outcome', scalar('captcha')],
                ['count', flowMap([['gte', scalar(3)]])],
              ]),
            ],
          ]),
          flowMap([['limit', scalar(60)]]),
        ]),
      ],
      ['nested', blockMap([['enabled', scalar(true)]])],
      ['emptyMap', blockMap([])],
    ]);
    expect(emitYaml(doc)).toBe(
      [
        'name: p',
        'list: [a, b]',
        'empty: []',
        'rules:',
        '  - name: r1',
        '    when: { outcome: captcha, count: { gte: 3 } }',
        '  - { limit: 60 }',
        'nested:',
        '  enabled: true',
        'emptyMap: {}',
        '',
      ].join('\n'),
    );
  });

  it('round-trips emitted documents through the parser', () => {
    const doc = blockMap([
      ['a', scalar("it's: 'quoted'")],
      ['b', scalarSeq(['10001', 200, true, null])],
      ['c', blockSeq([blockMap([['x', scalar('^/v\\d+')]])])],
    ]);
    expect(parseYaml(emitYaml(doc))).toEqual({
      a: "it's: 'quoted'",
      b: ['10001', 200, true, null],
      c: [{ x: '^/v\\d+' }],
    });
  });
});

describe('YAML subset parser', () => {
  it('strips comments outside quotes', () => {
    expect(stripComment('a: b # note')).toBe('a: b');
    expect(stripComment("a: 'b # not a comment'")).toBe("a: 'b # not a comment'");
    expect(stripComment('a: b#c')).toBe('a: b#c');
    expect(stripComment('# whole line')).toBe('');
  });

  it('parses block mappings, sequences, flow collections and scalars', () => {
    const text = [
      '---',
      '# Header comment',
      'name: default-signal',
      'trust_outcome_hint: false',
      'ratio: 0.4',
      'rules:',
      '  - name: proxy-error',
      '    when: { error_kind: [proxy_auth, conn_refused] }   # trailing comment',
      '    outcome: proxy_error',
      '  - when:',
      '      http_status: { gte: 200, lt: 300 }',
      '    outcome: success',
      'compact:',
      '- a',
      '- "b c"',
      'empty_list: []',
      'empty_map: {}',
      'nothing:',
      'multi: [one,',
      '  two]',
      '',
    ].join('\n');
    expect(parseYaml(text)).toEqual({
      name: 'default-signal',
      trust_outcome_hint: false,
      ratio: 0.4,
      rules: [
        { name: 'proxy-error', when: { error_kind: ['proxy_auth', 'conn_refused'] }, outcome: 'proxy_error' },
        { when: { http_status: { gte: 200, lt: 300 } }, outcome: 'success' },
      ],
      compact: ['a', 'b c'],
      empty_list: [],
      empty_map: {},
      nothing: null,
      multi: ['one', 'two'],
    });
  });

  it('parses block scalars and escapes', () => {
    const text = [
      'description: |-',
      '  first line',
      '  second line',
      'quoted: "tab\\tend"',
      'folded: >',
      '  a',
      '  b',
      '',
    ].join('\n');
    expect(parseYaml(text)).toEqual({
      description: 'first line\nsecond line',
      quoted: 'tab\tend',
      folded: 'a b\n',
    });
  });

  it('treats quotes and brackets inside plain scalars as text', () => {
    const text = [
      "description: Don't ban on the first captcha [yet]",
      "note: rock 'n' roll # comment",
      "said: it's 'quoted' # comment",
      "markers: [it's, 'a, b']",
      'rules: []',
      '',
    ].join('\n');
    expect(parseYaml(text)).toEqual({
      description: "Don't ban on the first captcha [yet]",
      note: "rock 'n' roll",
      said: "it's 'quoted'",
      markers: ["it's", 'a, b'],
      rules: [],
    });
  });

  it('folds block scalars like the server', () => {
    expect(parseYaml('x: >\n  a\n  b\n\n  c\n\n\n  d\n')).toEqual({ x: 'a b\nc\n\nd\n' });
    expect(parseYaml('x: >-\n    indented\n      more\n    back\n')).toEqual({ x: 'indented\n  more\nback' });
    expect(parseYaml('x: >+\n  a\n\n\ny: 1\n')).toEqual({ x: 'a\n\n\n', y: 1 });
    expect(parseYaml('x: |\n  a\n\n  b\n\n\ny: 2\n')).toEqual({ x: 'a\n\nb\n', y: 2 });
  });

  it('resolves numbers like the server and keeps literal codes', () => {
    expect(parseYaml('codes: [0010, 10001, 1_000, -0x1F, 0b101, 0o17, 12345678901234567890, 09]\n')).toEqual({
      codes: ['0010', 10001, 1000, -31, 5, 15, '12345678901234567890', '09'],
    });
    expect(parseYaml('a:\t1\nb:\t[x,\ty]\n')).toEqual({ a: 1, b: ['x', 'y'] });
  });

  it('rejects unsupported and invalid YAML with line numbers', () => {
    expect(() => parseYaml('a: &x 1\nb: *x')).toThrow(YamlSubsetError);
    expect(() => parseYaml('a: 1\na: 2')).toThrow(/line 2: duplicate key "a"/);
    expect(() => parseYaml('a: [1, 2')).toThrow(/unterminated/);
    expect(() => parseYaml('a:\n  b: 1\n   c: 2')).toThrow(/indentation/);
    expect(() => parseYaml('a: 1\n---\nb: 2')).toThrow(/multiple documents/);
  });

  it('never assigns prototypes from keys', () => {
    const parsed = parseYaml('__proto__: { polluted: true }') as Record<string, unknown>;
    expect(Object.getPrototypeOf(parsed)).toBe(Object.prototype);
    expect(Object.keys(parsed)).toEqual(['__proto__']);
  });
});
