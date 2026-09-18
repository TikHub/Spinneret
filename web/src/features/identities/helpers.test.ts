import { create } from '@bufbuild/protobuf';
import { describe, expect, it } from 'vitest';

import { IdentitySchema, ImportFailureSchema } from '@/gen/spinneret/v1/identity_admin_pb';

import {
  countImportRows,
  detectImportFormat,
  IMPORT_FAILURES_SHOWN,
  IMPORT_MAX_BYTES,
  sameImportInput,
  summarizeImport,
  utf8ByteLength,
  validateImportData,
} from './importData';
import { buildUpdateIdentityRequest, metadataFormFromIdentity, validateMetadata } from './metadata';
import { containsMaskedValues, maskedFieldNames, parsePayloadJson } from './payload';
import { fromDateTimeLocalValue, toDateTimeLocalValue, validateTimeRange } from './timeRange';

describe('import data helpers', () => {
  it('detects formats and counts rows', () => {
    expect(detectImportFormat('ids.CSV')).toBe('csv');
    expect(detectImportFormat('ids.ndjson')).toBe('jsonl');
    expect(detectImportFormat('ids.txt')).toBeUndefined();
    expect(countImportRows('a,b\n1,2\n\n3,4\n', 'csv')).toBe(2);
    expect(countImportRows('{"a":1}\r\n{"a":2}', 'jsonl')).toBe(2);
    expect(countImportRows('   ', 'jsonl')).toBe(0);
  });

  it('measures UTF-8 bytes and validates size', () => {
    expect(utf8ByteLength('abc')).toBe(3);
    expect(utf8ByteLength('é')).toBe(2);
    expect(utf8ByteLength('中')).toBe(3);
    expect(utf8ByteLength('😀')).toBe(4);
    expect(validateImportData('', 'jsonl')).toBe('empty');
    expect(validateImportData('x'.repeat(IMPORT_MAX_BYTES + 1), 'jsonl')).toBe('tooLarge');
    expect(validateImportData('{"a":1}', 'jsonl')).toBeUndefined();
  });

  it('matches dry run inputs', () => {
    const key = { site: 's', type: 't', format: 'csv' as const, mode: 'upsert' as const, dataVersion: 2 };
    expect(sameImportInput(key, { ...key })).toBe(true);
    expect(sameImportInput(key, { ...key, dataVersion: 3 })).toBe(false);
    expect(sameImportInput(undefined, key)).toBe(false);
  });

  it('summarizes results with sorted, capped failures', () => {
    const failed = Array.from({ length: IMPORT_FAILURES_SHOWN + 2 }, (_, i) =>
      create(ImportFailureSchema, { line: IMPORT_FAILURES_SHOWN + 2 - i, message: `bad ${i}` }),
    );
    const summary = summarizeImport({ created: 3, updated: 1, unchanged: 2, failed });
    expect(summary.total).toBe(6 + failed.length);
    expect(summary.failed).toBe(failed.length);
    expect(summary.shownFailures).toHaveLength(IMPORT_FAILURES_SHOWN);
    expect(summary.shownFailures[0]?.line).toBe(1);
    expect(summary.hiddenFailures).toBe(2);
  });
});

describe('payload helpers', () => {
  it('detects masked values deeply', () => {
    expect(containsMaskedValues({ a: { b: ['x', '••••1234'] } })).toBe(true);
    expect(containsMaskedValues({ a: 'plain', n: 1, z: null })).toBe(false);
    expect(maskedFieldNames({ cookies: { sid: '••••abcd' }, ua: 'Mozilla', token: '••••' })).toEqual([
      'cookies',
      'token',
    ]);
  });

  it('parses objects only', () => {
    expect(parsePayloadJson('{"a": 1}')).toEqual({ ok: true, value: { a: 1 } });
    expect(parsePayloadJson('[1]')).toEqual({ ok: false, error: 'notObject' });
    expect(parsePayloadJson('')).toEqual({ ok: false, error: 'empty' });
    expect(parsePayloadJson('{').ok).toBe(false);
  });
});

describe('metadata update request', () => {
  const identity = create(IdentitySchema, {
    id: 'idt_1',
    region: 'US',
    tags: ['a', 'b'],
    labels: { team: 'x' },
    accountRef: 'user-1',
  });

  it('returns undefined when nothing changed', () => {
    expect(buildUpdateIdentityRequest(identity, metadataFormFromIdentity(identity))).toBeUndefined();
  });

  it('includes only changed attributes, allowing clears', () => {
    const form = metadataFormFromIdentity(identity);
    expect(buildUpdateIdentityRequest(identity, { ...form, tags: [], accountRef: '' })).toEqual({
      id: 'idt_1',
      tags: [],
      setTags: true,
      accountRef: '',
    });
    expect(
      buildUpdateIdentityRequest(identity, {
        ...form,
        region: ' EU ',
        labels: [
          { key: 'team', value: 'y' },
          { key: 'env', value: 'prod' },
        ],
      }),
    ).toEqual({ id: 'idt_1', region: 'EU', labels: { team: 'y', env: 'prod' }, setLabels: true });
  });

  it('validates labels', () => {
    const form = metadataFormFromIdentity(identity);
    expect(
      validateMetadata({
        ...form,
        labels: [
          { key: 'a', value: '1' },
          { key: 'a', value: '2' },
        ],
      }),
    ).toEqual({ labels: 'duplicate' });
    expect(validateMetadata({ ...form, labels: [{ key: ' ', value: 'v' }] })).toEqual({ labels: 'emptyKey' });
    expect(validateMetadata(form)).toEqual({});
  });
});

describe('datetime-local helpers', () => {
  it('round-trips local times', () => {
    const ms = new Date(2026, 0, 2, 15, 4).getTime();
    expect(toDateTimeLocalValue(ms)).toBe('2026-01-02T15:04');
    expect(fromDateTimeLocalValue('2026-01-02T15:04')).toBe(ms);
    expect(fromDateTimeLocalValue('2026-02-30T10:00')).toBeUndefined();
    expect(fromDateTimeLocalValue('nope')).toBeUndefined();
  });

  it('validates ranges', () => {
    expect(validateTimeRange('', '')).toBe('startRequired');
    expect(validateTimeRange('2026-01-02T15:04', '')).toBeUndefined();
    expect(validateTimeRange('2026-01-02T15:04', '2026-01-02T15:00')).toBe('order');
    expect(validateTimeRange('2026-01-02T15:04', 'x')).toBe('endInvalid');
  });
});
