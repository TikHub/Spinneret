import { create } from '@bufbuild/protobuf';
import { describe, expect, it } from 'vitest';

import { IdentityFieldSchema } from '@/gen/spinneret/v1/identity_admin_pb';

import { buildNewIdentityRow, emptyFieldValues, type NewIdentityMeta } from './newIdentity';

const field = (name: string, type: string, required = false) =>
  create(IdentityFieldSchema, { name, type, required });

const NO_META: NewIdentityMeta = { account: '', region: '', tags: '' };

/** The douyin web type: a cookie map, a dedup key and the matching user agent. */
const DOUYIN = [
  field('cookies', 'cookie_map', true),
  field('user_id', 'string', true),
  field('user_agent', 'string', true),
];

describe('buildNewIdentityRow', () => {
  it('builds one JSON Lines row from the field values', () => {
    const result = buildNewIdentityRow(
      DOUYIN,
      {
        cookies: 'ttwid=1%7Cabc; sessionid=deadbeef',
        user_id: '95741160741',
        user_agent: 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:153.0) Gecko/20100101 Firefox/153.0',
      },
      NO_META,
    );

    expect(result.ok).toBe(true);
    if (!result.ok) return;
    expect(result.line.includes('\n')).toBe(false);
    expect(JSON.parse(result.line)).toEqual({
      cookies: 'ttwid=1%7Cabc; sessionid=deadbeef',
      user_id: '95741160741',
      user_agent: 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:153.0) Gecko/20100101 Firefox/153.0',
    });
  });

  it('reports every missing required field at once', () => {
    const result = buildNewIdentityRow(DOUYIN, { cookies: '  ', user_id: '', user_agent: 'UA' }, NO_META);
    expect(result.ok).toBe(false);
    if (result.ok) return;
    expect(result.errors).toEqual({ cookies: 'required', user_id: 'required' });
  });

  it('omits empty optional fields instead of sending an empty value', () => {
    const fields = [field('token', 'string', true), field('note', 'string')];
    const result = buildNewIdentityRow(fields, { token: 't', note: '   ' }, NO_META);
    expect(result.ok).toBe(true);
    if (!result.ok) return;
    expect(JSON.parse(result.line)).toEqual({ token: 't' });
  });

  it('coerces numbers and booleans to their JSON types', () => {
    const fields = [field('retries', 'number'), field('verified', 'bool'), field('off', 'bool')];
    const result = buildNewIdentityRow(fields, { retries: '12', verified: 'true', off: 'false' }, NO_META);
    expect(result.ok).toBe(true);
    if (!result.ok) return;
    expect(JSON.parse(result.line)).toEqual({ retries: 12, verified: true, off: false });
  });

  it('rejects a number that is not one', () => {
    const fields = [field('retries', 'number')];
    expect(buildNewIdentityRow(fields, { retries: 'many' }, NO_META)).toMatchObject({
      ok: false,
      errors: { retries: 'notANumber' },
    });
    expect(buildNewIdentityRow(fields, { retries: 'Infinity' }, NO_META)).toMatchObject({
      ok: false,
      errors: { retries: 'notANumber' },
    });
  });

  it('keeps a cookie header as a string but decodes a browser export', () => {
    const fields = [field('cookies', 'cookie_map', true)];
    const header = buildNewIdentityRow(fields, { cookies: 'a=1; b=2' }, NO_META);
    expect(header.ok && JSON.parse(header.line).cookies).toBe('a=1; b=2');

    const exported = buildNewIdentityRow(
      fields,
      { cookies: '[{"name":"a","value":"1","domain":".douyin.com"}]' },
      NO_META,
    );
    expect(exported.ok && JSON.parse(exported.line).cookies).toEqual([
      { name: 'a', value: '1', domain: '.douyin.com' },
    ]);
  });

  it('does not mistake a cookie value that merely starts with a brace for JSON', () => {
    const fields = [field('cookies', 'cookie_map', true)];
    const result = buildNewIdentityRow(fields, { cookies: '{not json; a=1' }, NO_META);
    expect(result.ok && JSON.parse(result.line).cookies).toBe('{not json; a=1');
  });

  it('parses json fields and reports invalid JSON', () => {
    const fields = [field('extra', 'json')];
    const good = buildNewIdentityRow(fields, { extra: '{"device":{"id":7}}' }, NO_META);
    expect(good.ok && JSON.parse(good.line).extra).toEqual({ device: { id: 7 } });
    expect(buildNewIdentityRow(fields, { extra: '{oops' }, NO_META)).toMatchObject({
      ok: false,
      errors: { extra: 'invalidJson' },
    });
  });

  it('does not read a string value as an error code', () => {
    const fields = [field('note', 'string', true)];
    const result = buildNewIdentityRow(fields, { note: 'invalidJson' }, NO_META);
    expect(result.ok).toBe(true);
    if (!result.ok) return;
    expect(JSON.parse(result.line).note).toBe('invalidJson');
  });

  it('treats an untouched switch as false rather than a missing field', () => {
    const fields = [field('verified', 'bool', true)];
    const result = buildNewIdentityRow(fields, emptyFieldValues(fields), NO_META);
    expect(result.ok).toBe(true);
    if (!result.ok) return;
    expect(JSON.parse(result.line)).toEqual({ verified: false });
  });

  it('carries the reserved metadata keys and splits tags', () => {
    const result = buildNewIdentityRow(
      DOUYIN,
      {
        cookies: 'a=1',
        user_id: '1',
        user_agent: 'UA',
      },
      { account: 'douyin_web_user_id_95741160741', region: 'us-west', tags: 'warm, paid ,,warm' },
    );

    expect(result.ok).toBe(true);
    if (!result.ok) return;
    const row = JSON.parse(result.line);
    expect(row._account).toBe('douyin_web_user_id_95741160741');
    expect(row._region).toBe('us-west');
    expect(row._tags).toEqual(['warm', 'paid']);
  });

  it('leaves out metadata keys that were not filled in', () => {
    const result = buildNewIdentityRow(DOUYIN, { cookies: 'a=1', user_id: '1', user_agent: 'UA' }, NO_META);
    expect(result.ok).toBe(true);
    if (!result.ok) return;
    expect(Object.keys(JSON.parse(result.line)).sort()).toEqual(['cookies', 'user_agent', 'user_id']);
  });

  it('refuses a field name the import format reserves', () => {
    const fields = [field('_account', 'string', true)];
    expect(buildNewIdentityRow(fields, { _account: 'x' }, NO_META)).toMatchObject({
      ok: false,
      errors: { _account: 'reserved' },
    });
  });

  it('escapes newlines so a pasted value cannot become a second row', () => {
    const fields = [field('note', 'string', true)];
    const result = buildNewIdentityRow(fields, { note: 'line one\nline two' }, NO_META);
    expect(result.ok).toBe(true);
    if (!result.ok) return;
    expect(result.line.includes('\n')).toBe(false);
    expect(JSON.parse(result.line).note).toBe('line one\nline two');
  });
});

describe('emptyFieldValues', () => {
  it('starts every field blank', () => {
    expect(emptyFieldValues(DOUYIN)).toEqual({ cookies: '', user_id: '', user_agent: '' });
  });
});
