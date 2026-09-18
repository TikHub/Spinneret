import { create } from '@bufbuild/protobuf';
import { describe, expect, it } from 'vitest';

import { ProxySchema } from '@/gen/spinneret/v1/proxy_admin_pb';

import {
  buildUpdateProxyRequest,
  isEmptyUpdate,
  proxyToEditForm,
  validateProxyEditForm,
  validateProxyUrl,
  validateSessionTemplate,
} from './proxyForm';

const proxy = create(ProxySchema, {
  id: 'pxy_1',
  kind: 'residential',
  region: 'US',
  city: 'NYC',
  provider: 'acme',
  maxConcurrency: 4,
  tags: ['a', 'b'],
  sessionTemplate: 'user-{username}',
});

describe('validateSessionTemplate', () => {
  it('accepts known placeholders and plain text', () => {
    expect(validateSessionTemplate('')).toBeUndefined();
    expect(
      validateSessionTemplate(
        '{username}-session-{identity_hash}-{random}:{password}{lease_id}{identity_id}',
      ),
    ).toBeUndefined();
  });

  it('reports malformed templates', () => {
    expect(validateSessionTemplate('user-{name}')).toEqual({ code: 'unknown_placeholder', name: 'name' });
    expect(validateSessionTemplate('user-{username')).toEqual({ code: 'unterminated' });
    expect(validateSessionTemplate('user-{user{name}}')).toEqual({ code: 'unterminated' });
    expect(validateSessionTemplate('user}')).toEqual({ code: 'unmatched_close' });
    expect(validateSessionTemplate('x'.repeat(513))).toEqual({ code: 'too_long' });
  });
});

describe('validateProxyUrl', () => {
  it('accepts http, https and socks5 URLs with a port', () => {
    expect(validateProxyUrl('http://203.0.113.10:8000')).toBeUndefined();
    expect(validateProxyUrl('socks5://user:p%40ss@proxy.example.com:1080')).toBeUndefined();
    expect(validateProxyUrl('HTTPS://[2001:db8::1]:443/')).toBeUndefined();
  });

  it('splits the user info at the last "@" like the server (unencoded "@" in a password)', () => {
    expect(validateProxyUrl('http://user:p@ss@proxy.example.com:8080')).toBeUndefined();
    expect(validateProxyUrl('http://user:p@ss@proxy.example.com')).toBe('port');
    expect(validateProxyUrl('http://user:pa/ss@proxy.example.com:8080')).toBe('format');
  });

  it('reports the problem without echoing the input', () => {
    expect(validateProxyUrl('  ')).toBe('required');
    expect(validateProxyUrl('ftp://host:21')).toBe('scheme');
    expect(validateProxyUrl('host:8080')).toBe('scheme');
    expect(validateProxyUrl('http://host')).toBe('port');
    expect(validateProxyUrl('http://host:70000')).toBe('port');
    expect(validateProxyUrl('http://host:80/path')).toBe('format');
  });
});

describe('proxy edit form', () => {
  it('builds an empty update when nothing changed', () => {
    const req = buildUpdateProxyRequest(proxy, proxyToEditForm(proxy));
    expect(req).toEqual({ id: 'pxy_1', tags: [], setTags: false });
    expect(isEmptyUpdate(req)).toBe(true);
  });

  it('sends only changed fields, tags with set_tags and trimmed values', () => {
    const form = {
      ...proxyToEditForm(proxy),
      region: ' DE ',
      maxConcurrency: '8',
      tags: ['a'],
      sessionTemplate: '',
      replaceUrl: true,
      url: ' http://u:p@host:1 ',
      urlConfirm: 'http://u:p@host:1',
    };
    expect(validateProxyEditForm(form)).toEqual({});
    const req = buildUpdateProxyRequest(proxy, form);
    expect(req).toEqual({
      id: 'pxy_1',
      url: 'http://u:p@host:1',
      region: 'DE',
      maxConcurrency: 8,
      tags: ['a'],
      setTags: true,
      sessionTemplate: '',
    });
    expect(isEmptyUpdate(req)).toBe(false);
  });

  it('allows clearing all tags', () => {
    const req = buildUpdateProxyRequest(proxy, { ...proxyToEditForm(proxy), tags: [] });
    expect(req).toMatchObject({ tags: [], setTags: true });
  });

  it('validates the URL confirmation, concurrency and template', () => {
    const issues = validateProxyEditForm({
      ...proxyToEditForm(proxy),
      replaceUrl: true,
      url: 'http://host:1',
      urlConfirm: 'http://host:2',
      maxConcurrency: '0',
      sessionTemplate: '{bad}',
    });
    expect(issues).toEqual({
      urlConfirm: 'mismatch',
      maxConcurrency: 'range',
      sessionTemplate: { code: 'unknown_placeholder', name: 'bad' },
    });
  });
});
