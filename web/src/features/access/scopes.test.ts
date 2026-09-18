import { describe, expect, it } from 'vitest';

import {
  applyPreset,
  buildScope,
  createScopeRow,
  expandPreset,
  parseScope,
  rowsFromScopes,
  SCOPE_PRESETS,
  scopesFromRows,
  TOKEN_SCOPE_NAMES,
  validateScopeArg,
  validateScopeRows,
} from './scopes';

describe('buildScope', () => {
  it('omits empty arguments and arguments of scopes without one', () => {
    expect(buildScope('lease:acquire', '')).toBe('lease:acquire');
    expect(buildScope('lease:acquire', '  shop ')).toBe('lease:acquire:shop');
    expect(buildScope('config:read', 'crawler*')).toBe('config:read:crawler*');
    expect(buildScope('secret:read', 'prod/signing/*')).toBe('secret:read:prod/signing/*');
    expect(buildScope('proxy:write', 'ignored')).toBe('proxy:write');
    expect(buildScope('admin', 'ignored')).toBe('admin');
  });
});

describe('parseScope', () => {
  it('splits known scopes into name and argument', () => {
    expect(parseScope('lease:acquire')).toMatchObject({ name: 'lease:acquire', arg: '', known: true });
    expect(parseScope('report:write:shop')).toMatchObject({
      name: 'report:write',
      arg: 'shop',
      known: true,
    });
    expect(parseScope('secret:read:prod/signing/*')).toMatchObject({
      name: 'secret:read',
      arg: 'prod/signing/*',
    });
    expect(parseScope('admin')).toMatchObject({ name: 'admin', arg: '', known: true });
  });

  it('keeps colons inside the argument', () => {
    expect(parseScope('config:read:a:b')).toMatchObject({ name: 'config:read', arg: 'a:b' });
  });

  it('marks unknown scopes and arguments on scopes without one', () => {
    expect(parseScope('lease:acquirex')).toMatchObject({ known: false, name: 'lease:acquirex' });
    expect(parseScope('something')).toMatchObject({ known: false });
    expect(parseScope('admin:ns')).toMatchObject({ known: false, name: 'admin:ns' });
  });

  it('round-trips every scope built by the builder', () => {
    const samples: Record<string, string> = {
      'lease:acquire': 'shop',
      'report:write': 'market',
      'config:read': 'crawler*',
      'config:publish': 'crawler.*',
      'secret:read': 'prod/signing/*',
      'identity:write': 'shop',
      'proxy:write': '',
      admin: '',
    };
    for (const name of TOKEN_SCOPE_NAMES) {
      const scope = buildScope(name, samples[name] ?? '');
      const parsed = parseScope(scope);
      expect(parsed.known).toBe(true);
      expect(buildScope(parsed.name as typeof name, parsed.arg)).toBe(scope);
    }
  });
});

describe('scopesFromRows / rowsFromScopes', () => {
  it('builds unique scope strings in order', () => {
    const rows = [
      createScopeRow('lease:acquire', 'shop'),
      createScopeRow('report:write'),
      createScopeRow('lease:acquire', 'shop'),
      createScopeRow('admin'),
    ];
    expect(scopesFromRows(rows)).toEqual(['lease:acquire:shop', 'report:write', 'admin']);
  });

  it('parses scopes back into rows with unique IDs, skipping unknown scopes', () => {
    const rows = rowsFromScopes(['config:read:crawler*', 'unknown', 'secret:read:prod/x/*']);
    expect(rows.map((r) => [r.name, r.arg])).toEqual([
      ['config:read', 'crawler*'],
      ['secret:read', 'prod/x/*'],
    ]);
    expect(new Set(rows.map((r) => r.id)).size).toBe(2);
  });
});

describe('validation', () => {
  it('requires a path glob for secret:read', () => {
    expect(validateScopeArg('secret:read', '')).toBe('argRequired');
    expect(validateScopeArg('secret:read', 'prod/signing/*')).toBeUndefined();
    expect(validateScopeArg('secret:read', 'no-slash')).toBe('argInvalid');
    expect(validateScopeArg('secret:read', '/abs/path')).toBe('argInvalid');
    expect(validateScopeArg('secret:read', 'prod/with space')).toBe('argInvalid');
  });

  it('accepts optional arguments and rejects invalid globs', () => {
    expect(validateScopeArg('lease:acquire', '')).toBeUndefined();
    expect(validateScopeArg('config:read', 'crawler*')).toBeUndefined();
    expect(validateScopeArg('config:read', 'bad/group')).toBe('argInvalid');
    expect(validateScopeArg('lease:acquire', 'bad site')).toBe('argInvalid');
    // Site arguments are exact names: the server rejects wildcards.
    expect(validateScopeArg('lease:acquire', 'shop*')).toBe('argInvalid');
    expect(validateScopeArg('identity:write', 'sh?p')).toBe('argInvalid');
    expect(validateScopeArg('report:write', 'Shop')).toBe('argInvalid');
    expect(validateScopeArg('config:read', 'crawler[12]')).toBe('argInvalid');
    expect(validateScopeArg('config:publish', '_internal.*')).toBeUndefined();
    expect(validateScopeArg('proxy:write', 'whatever')).toBeUndefined();
    expect(validateScopeArg('config:read', 'x'.repeat(300))).toBe('tooLong');
  });

  it('flags duplicate rows after the first occurrence', () => {
    const a = createScopeRow('lease:acquire');
    const b = createScopeRow('lease:acquire', ' ');
    const c = createScopeRow('secret:read');
    expect(validateScopeRows([a, b, c])).toEqual({ [b.id]: 'duplicate', [c.id]: 'argRequired' });
  });
});

describe('presets', () => {
  it('expands presets to scope strings', () => {
    expect(expandPreset('crawlerNode')).toEqual(['lease:acquire', 'report:write', 'config:read']);
    expect(expandPreset('cookieRefresher')).toEqual(['identity:write']);
    expect(expandPreset('ciPublisher')).toEqual(['config:publish']);
    expect(SCOPE_PRESETS).toHaveLength(3);
  });

  it('appends only missing scopes', () => {
    const existing = [createScopeRow('report:write'), createScopeRow('lease:acquire', 'shop')];
    const rows = applyPreset(existing, 'crawlerNode');
    expect(scopesFromRows(rows)).toEqual([
      'report:write',
      'lease:acquire:shop',
      'lease:acquire',
      'config:read',
    ]);
    expect(rows.slice(0, 2)).toEqual(existing);
    expect(scopesFromRows(applyPreset(rows, 'crawlerNode'))).toHaveLength(4);
  });
});
