import { create } from '@bufbuild/protobuf';
import { ValueSchema } from '@bufbuild/protobuf/wkt';
import { describe, expect, it } from 'vitest';

import { CredentialSchema } from '@/gen/spinneret/v1/lease_pb';

import { credentialView, isSegmentUsed, samplePayload } from './credential';
import { splitSpecErrors, specErrorMarkers } from './specErrors';

describe('specErrors', () => {
  it('splits joined yaml type errors and maps lines to markers', () => {
    const messages = [
      'parse identity type yaml: line 5: field foo not found in type identity.TypeSpec; line 9: cannot unmarshal !!seq',
      'unique_by: path "cookies.x" refers to unknown field "cookies"',
    ];
    expect(splitSpecErrors(messages)).toEqual([
      'parse identity type yaml: line 5: field foo not found in type identity.TypeSpec',
      'line 9: cannot unmarshal !!seq',
      'unique_by: path "cookies.x" refers to unknown field "cookies"',
    ]);
    const markers = specErrorMarkers(messages, 7);
    expect(markers).toEqual([
      {
        line: 5,
        column: undefined,
        message: 'parse identity type yaml: line 5: field foo not found in type identity.TypeSpec',
        severity: 'error',
      },
      { line: 7, column: undefined, message: 'line 9: cannot unmarshal !!seq', severity: 'error' },
    ]);
  });

  it('splits the server problem list joined with "; " but not quoted values', () => {
    expect(
      splitSpecErrors([
        'fields.cookies: unknown type "a; b"; unique_by: duplicate path "x"; and 3 more problems',
      ]),
    ).toEqual([
      'fields.cookies: unknown type "a; b"',
      'unique_by: duplicate path "x"',
      'and 3 more problems',
    ]);
    expect(splitSpecErrors(['name "say \\"hi; there\\"" must match ^[a-z]+$'])).toEqual([
      'name "say \\"hi; there\\"" must match ^[a-z]+$',
    ]);
  });

  it('reads columns and ignores messages without lines', () => {
    expect(specErrorMarkers(['yaml: line 3, column 7: mapping values are not allowed'])).toMatchObject([
      { line: 3, column: 7 },
    ]);
    expect(specErrorMarkers(['name is required'])).toEqual([]);
  });
});

describe('credentialView', () => {
  it('sorts map segments and converts json', () => {
    const view = credentialView(
      create(CredentialSchema, {
        cookies: { b: '2', a: '1' },
        cookieHeader: 'a=1; b=2',
        headers: { 'User-Agent': 'x' },
        json: create(ValueSchema, { kind: { case: 'stringValue', value: 'hi' } }),
        values: { signature: 't' },
      }),
    );
    expect(view.cookies).toEqual([
      ['a', '1'],
      ['b', '2'],
    ]);
    expect(view.json).toBe('hi');
    expect(isSegmentUsed(view, 'cookies')).toBe(true);
    expect(isSegmentUsed(view, 'query')).toBe(false);
    expect(isSegmentUsed(view, 'json')).toBe(true);
    expect(isSegmentUsed(view, 'values')).toBe(true);
  });

  it('handles a missing credential', () => {
    const view = credentialView(undefined);
    expect(view.json).toBeNull();
    expect(isSegmentUsed(view, 'cookie_header')).toBe(false);
  });

  it('builds sample payloads per field type', () => {
    expect(
      samplePayload([
        { name: 'cookies', type: 'cookie_map' },
        { name: 'n', type: 'number' },
        { name: 'ok', type: 'bool' },
        { name: 'ua', type: 'string' },
      ]),
    ).toEqual({
      cookies: { sessionid: 'example-session', csrf_token: 'example-csrf' },
      n: 0,
      ok: false,
      ua: 'example-ua',
    });
  });
});
