import { describe, expect, it } from 'vitest';

import {
  buildImportDefaults,
  countDataLines,
  EMPTY_IMPORT_DEFAULTS,
  formatFromFileName,
  importFingerprint,
  summarizeImportData,
  validateImport,
} from './importProxies';

describe('import helpers', () => {
  it('detects the format from the file name', () => {
    expect(formatFromFileName('pool.CSV')).toBe('csv');
    expect(formatFromFileName('pool.jsonl')).toBe('jsonl');
    expect(formatFromFileName('pool.txt')).toBe('lines');
    expect(formatFromFileName('pool.bin')).toBeUndefined();
  });

  it('counts data lines per format', () => {
    expect(countDataLines('# comment\nhttp://a:1\n\nhttp://b:2\n', 'lines')).toBe(2);
    expect(countDataLines('url,kind\nhttp://a:1,mobile\n', 'csv')).toBe(1);
    expect(countDataLines('{"url":"x"}\n{"url":"y"}', 'jsonl')).toBe(2);
  });

  it('builds defaults with trimmed values and 0 for an empty concurrency', () => {
    expect(
      buildImportDefaults({ ...EMPTY_IMPORT_DEFAULTS, region: ' US ', tags: ['t'], kind: 'mobile' }),
    ).toEqual({
      kind: 'mobile',
      region: 'US',
      city: '',
      provider: '',
      tags: ['t'],
      maxConcurrency: 0,
      sessionTemplate: '',
    });
    expect(buildImportDefaults({ ...EMPTY_IMPORT_DEFAULTS, maxConcurrency: '5' }).maxConcurrency).toBe(5);
  });

  it('validates data and defaults', () => {
    expect(validateImport(summarizeImportData(' \n\t'), EMPTY_IMPORT_DEFAULTS)).toEqual(['data_required']);
    expect(
      validateImport(summarizeImportData('http://a:1'), {
        ...EMPTY_IMPORT_DEFAULTS,
        maxConcurrency: 'x',
        sessionTemplate: '{x}',
      }),
    ).toEqual(['max_concurrency', 'session_template']);
    const huge = { ...summarizeImportData('x'), bytes: 32 * 1024 * 1024 + 1 };
    expect(validateImport(huge, EMPTY_IMPORT_DEFAULTS)).toEqual(['data_too_large']);
    expect(summarizeImportData('é')).toMatchObject({ blank: false, bytes: 2, length: 1 });
  });

  it('changes the fingerprint when data or defaults change', () => {
    const data = summarizeImportData('http://a:1');
    const a = importFingerprint('lines', data, EMPTY_IMPORT_DEFAULTS);
    expect(importFingerprint('lines', summarizeImportData('http://a:1'), EMPTY_IMPORT_DEFAULTS)).toBe(a);
    expect(importFingerprint('lines', summarizeImportData('http://a:2'), EMPTY_IMPORT_DEFAULTS)).not.toBe(a);
    expect(importFingerprint('csv', data, EMPTY_IMPORT_DEFAULTS)).not.toBe(a);
    expect(importFingerprint('lines', data, { ...EMPTY_IMPORT_DEFAULTS, region: 'US' })).not.toBe(a);
  });
});
