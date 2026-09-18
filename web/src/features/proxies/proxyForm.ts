import { type Proxy } from '@/gen/spinneret/v1/proxy_admin_pb';

import { PROXY_KINDS } from './proxyFilters';

/** Attribute limits (mirrors internal/proxy/attrs.go and the proto validation). */
export const PROXY_LIMITS = {
  url: 2048,
  region: 64,
  city: 128,
  provider: 128,
  tag: 64,
  tags: 64,
  maxConcurrency: 100_000,
  sessionTemplate: 512,
} as const;

/** Placeholders of a tunnel session template (internal/proxy/template.go). */
export const SESSION_PLACEHOLDERS = [
  'username',
  'password',
  'identity_id',
  'identity_hash',
  'lease_id',
  'random',
] as const;

export type SessionTemplateIssue =
  | { code: 'too_long' }
  | { code: 'unmatched_close' }
  | { code: 'unterminated' }
  | { code: 'unknown_placeholder'; name: string };

const encoder = new TextEncoder();

/** UTF-8 byte length (the backend limits are in bytes). */
export function byteLength(value: string): number {
  return encoder.encode(value).length;
}

/** Validates a session template like the backend; undefined when valid (empty is valid). */
export function validateSessionTemplate(template: string): SessionTemplateIssue | undefined {
  if (byteLength(template) > PROXY_LIMITS.sessionTemplate) return { code: 'too_long' };
  let rest = template;
  while (rest !== '') {
    const open = rest.search(/[{}]/);
    if (open < 0) return undefined;
    if (rest[open] === '}') return { code: 'unmatched_close' };
    const after = rest.slice(open + 1);
    const end = after.search(/[{}]/);
    if (end < 0 || after[end] !== '}') return { code: 'unterminated' };
    const name = after.slice(0, end);
    if (!(SESSION_PLACEHOLDERS as readonly string[]).includes(name)) {
      return { code: 'unknown_placeholder', name: name.slice(0, 32) };
    }
    rest = after.slice(end + 1);
  }
  return undefined;
}

export type ProxyUrlIssue = 'required' | 'too_long' | 'scheme' | 'format' | 'port';

// The user info ends at the last "@" of the authority (like Go's net/url), so an
// unencoded "@" inside a password is accepted; the host itself never contains "@".
const URL_SHAPE = /^([a-z0-9+.-]+):\/\/(?:[^/?#\s]*@)?(\[[0-9a-f:.]+\]|[^:/@?#[\]\s]+)(?::(\d*))?\/?$/i;

/**
 * Validates a proxy URL of the form scheme://[user[:password]@]host:port with
 * scheme http, https or socks5 and an explicit port (internal/proxy/url.go).
 * The server remains authoritative; messages never include the input.
 */
export function validateProxyUrl(raw: string): ProxyUrlIssue | undefined {
  const value = raw.trim();
  if (value === '') return 'required';
  if (byteLength(value) > PROXY_LIMITS.url) return 'too_long';
  const match = URL_SHAPE.exec(value);
  if (!match) return value.includes('://') ? 'format' : 'scheme';
  const scheme = (match[1] ?? '').toLowerCase();
  if (scheme !== 'http' && scheme !== 'https' && scheme !== 'socks5') return 'scheme';
  const port = match[3];
  if (port === undefined || port === '') return 'port';
  const n = Number(port);
  if (!Number.isInteger(n) || n < 1 || n > 65535) return 'port';
  return undefined;
}

/** Edit dialog state. */
export interface ProxyEditForm {
  replaceUrl: boolean;
  url: string;
  urlConfirm: string;
  kind: string;
  region: string;
  city: string;
  provider: string;
  maxConcurrency: string;
  tags: string[];
  sessionTemplate: string;
}

export function proxyToEditForm(proxy: Proxy): ProxyEditForm {
  return {
    replaceUrl: false,
    url: '',
    urlConfirm: '',
    kind: proxy.kind || 'datacenter',
    region: proxy.region,
    city: proxy.city,
    provider: proxy.provider,
    maxConcurrency: String(proxy.maxConcurrency || 1),
    tags: [...proxy.tags],
    sessionTemplate: proxy.sessionTemplate,
  };
}

/** Error codes per field; the dialog translates them. */
export interface ProxyEditIssues {
  url?: ProxyUrlIssue;
  urlConfirm?: 'mismatch';
  kind?: 'invalid';
  region?: 'too_long';
  city?: 'too_long';
  provider?: 'too_long';
  maxConcurrency?: 'range';
  sessionTemplate?: SessionTemplateIssue;
}

export function parseMaxConcurrency(value: string): number | undefined {
  const trimmed = value.trim();
  if (!/^\d+$/.test(trimmed)) return undefined;
  const n = Number(trimmed);
  return n >= 1 && n <= PROXY_LIMITS.maxConcurrency ? n : undefined;
}

export function validateProxyEditForm(form: ProxyEditForm): ProxyEditIssues {
  const issues: ProxyEditIssues = {};
  if (form.replaceUrl) {
    issues.url = validateProxyUrl(form.url);
    if (!issues.url && form.url.trim() !== form.urlConfirm.trim()) issues.urlConfirm = 'mismatch';
  }
  if (!(PROXY_KINDS as readonly string[]).includes(form.kind)) issues.kind = 'invalid';
  if (byteLength(form.region.trim()) > PROXY_LIMITS.region) issues.region = 'too_long';
  if (byteLength(form.city.trim()) > PROXY_LIMITS.city) issues.city = 'too_long';
  if (byteLength(form.provider.trim()) > PROXY_LIMITS.provider) issues.provider = 'too_long';
  if (parseMaxConcurrency(form.maxConcurrency) === undefined) issues.maxConcurrency = 'range';
  issues.sessionTemplate = validateSessionTemplate(form.sessionTemplate.trim());
  return Object.fromEntries(Object.entries(issues).filter(([, v]) => v !== undefined)) as ProxyEditIssues;
}

export function hasIssues(issues: object): boolean {
  return Object.keys(issues).length > 0;
}

/** Plain init object of UpdateProxyRequest. */
export interface UpdateProxyInit {
  id: string;
  url?: string;
  kind?: string;
  region?: string;
  city?: string;
  provider?: string;
  maxConcurrency?: number;
  tags: string[];
  setTags: boolean;
  sessionTemplate?: string;
}

function sameList(a: readonly string[], b: readonly string[]): boolean {
  return a.length === b.length && a.every((v, i) => v === b[i]);
}

/** Builds an UpdateProxy request containing only the fields that changed. */
export function buildUpdateProxyRequest(proxy: Proxy, form: ProxyEditForm): UpdateProxyInit {
  const req: UpdateProxyInit = { id: proxy.id, tags: [], setTags: false };
  if (form.replaceUrl && form.url.trim() !== '') req.url = form.url.trim();
  if (form.kind !== proxy.kind) req.kind = form.kind;
  if (form.region.trim() !== proxy.region) req.region = form.region.trim();
  if (form.city.trim() !== proxy.city) req.city = form.city.trim();
  if (form.provider.trim() !== proxy.provider) req.provider = form.provider.trim();
  const maxConcurrency = parseMaxConcurrency(form.maxConcurrency);
  if (maxConcurrency !== undefined && maxConcurrency !== proxy.maxConcurrency) {
    req.maxConcurrency = maxConcurrency;
  }
  if (!sameList(form.tags, proxy.tags)) {
    req.tags = [...form.tags];
    req.setTags = true;
  }
  if (form.sessionTemplate.trim() !== proxy.sessionTemplate)
    req.sessionTemplate = form.sessionTemplate.trim();
  return req;
}

/** True when the request changes nothing. */
export function isEmptyUpdate(req: UpdateProxyInit): boolean {
  return (
    req.url === undefined &&
    req.kind === undefined &&
    req.region === undefined &&
    req.city === undefined &&
    req.provider === undefined &&
    req.maxConcurrency === undefined &&
    !req.setTags &&
    req.sessionTemplate === undefined
  );
}
