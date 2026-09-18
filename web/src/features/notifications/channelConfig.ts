import { type JsonObject, type JsonValue } from '@bufbuild/protobuf';

import { duplicateKeys, type KeyValuePair } from '@/lib/keyValue';

/** Channel kinds (CreateChannelRequest.kind). */
export const CHANNEL_KINDS = ['webhook', 'feishu', 'dingtalk', 'wecom', 'telegram'] as const;
export type ChannelKind = (typeof CHANNEL_KINDS)[number];

/** Alert kinds a channel can subscribe to. */
export const ALERT_KINDS = [
  'breaker_opened',
  'breaker_reopened',
  'breaker_closed',
  'identity_low_watermark',
  'proxy_low_watermark',
  'ban_spike',
  'report_backlog',
  'unknown_ratio_high',
  'client_error_spike',
  'identity_expired',
  'secret_expiring',
  'test',
] as const;
export type AlertKind = (typeof ALERT_KINDS)[number];

export const SEVERITIES = ['info', 'warning', 'critical'] as const;
export type Severity = (typeof SEVERITIES)[number];
/** Server default when min_severity is empty. */
export const DEFAULT_MIN_SEVERITY: Severity = 'warning';

/** Prefix of masked values returned by the server (internal/identity.MaskPrefix). */
export const MASK_PREFIX = '••••';

export const MAX_CHANNEL_NAME_LENGTH = 64;
export const MAX_CHANNEL_HEADERS = 20;
const BOT_TOKEN_PATTERN = /^[0-9]{1,20}:[A-Za-z0-9_-]{1,200}$/;
const HEADER_NAME_PATTERN = /^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/;
const RESERVED_HEADERS = new Set([
  'host',
  'content-length',
  'content-type',
  'transfer-encoding',
  'connection',
  'te',
  'upgrade',
  'trailer',
  'x-spinneret-timestamp',
  'x-spinneret-signature',
]);

export function isChannelKind(value: string): value is ChannelKind {
  return (CHANNEL_KINDS as readonly string[]).includes(value);
}

/** Form fields of the kind-specific channel settings. */
export interface ChannelConfigForm {
  url: string;
  secret: string;
  headers: KeyValuePair[];
  webhookUrl: string;
  botToken: string;
  chatId: string;
  apiBase: string;
}

export type ChannelConfigField = keyof ChannelConfigForm;

/** Fields shown per kind, in display order. */
export const CHANNEL_FIELDS: Record<ChannelKind, readonly ChannelConfigField[]> = {
  webhook: ['url', 'secret', 'headers'],
  feishu: ['webhookUrl', 'secret'],
  dingtalk: ['webhookUrl', 'secret'],
  wecom: ['webhookUrl'],
  telegram: ['botToken', 'chatId', 'apiBase'],
};

/**
 * Fields whose values the server masks on read (replace-only in the form).
 * api_base is masked only when it embeds credentials (user info).
 */
export const MASKABLE_FIELDS: ReadonlySet<ChannelConfigField> = new Set([
  'url',
  'webhookUrl',
  'secret',
  'botToken',
  'apiBase',
]);

/** Telegram API base used when a channel sets none (notify.DefaultTelegramAPIBase). */
export const DEFAULT_TELEGRAM_API_BASE = 'https://api.telegram.org';

/** Required fields per kind. */
const REQUIRED_FIELDS: ReadonlySet<ChannelConfigField> = new Set(['url', 'webhookUrl', 'botToken', 'chatId']);

/** JSON names of the form fields. */
const JSON_NAMES: Record<Exclude<ChannelConfigField, 'headers'>, string> = {
  url: 'url',
  secret: 'secret',
  webhookUrl: 'webhook_url',
  botToken: 'bot_token',
  chatId: 'chat_id',
  apiBase: 'api_base',
};

export const EMPTY_CHANNEL_CONFIG: ChannelConfigForm = {
  url: '',
  secret: '',
  headers: [],
  webhookUrl: '',
  botToken: '',
  chatId: '',
  apiBase: '',
};

/** Reports whether a value is (or contains) a server-masked value. */
export function isMaskedValue(value: string): boolean {
  return value.includes(MASK_PREFIX);
}

function stringValue(value: JsonValue | undefined): string {
  if (typeof value === 'string') return value;
  if (typeof value === 'number' || typeof value === 'boolean') return String(value);
  return '';
}

/** Converts channel settings (Struct as read from the server, masked) to form values. */
export function configToForm(config: JsonObject | undefined): ChannelConfigForm {
  if (!config) return EMPTY_CHANNEL_CONFIG;
  const rawHeaders = config.headers;
  const headers =
    rawHeaders && typeof rawHeaders === 'object' && !Array.isArray(rawHeaders)
      ? Object.entries(rawHeaders)
          .map(([key, value]) => ({ key, value: stringValue(value) }))
          .sort((a, b) => a.key.localeCompare(b.key))
      : [];
  return {
    url: stringValue(config.url),
    secret: stringValue(config.secret),
    headers,
    webhookUrl: stringValue(config.webhook_url),
    botToken: stringValue(config.bot_token),
    chatId: stringValue(config.chat_id),
    apiBase: stringValue(config.api_base),
  };
}

/**
 * Converts form values to channel settings for a kind: only the kind's fields,
 * trimmed, optional empty fields omitted. Untouched masked values are sent
 * back verbatim; the server keeps the stored value for them.
 */
export function formToConfig(kind: ChannelKind, form: ChannelConfigForm): JsonObject {
  const out: JsonObject = {};
  for (const field of CHANNEL_FIELDS[kind]) {
    if (field === 'headers') {
      const headers: JsonObject = {};
      for (const { key, value } of form.headers) {
        if (key.trim() !== '') headers[key.trim()] = value;
      }
      if (Object.keys(headers).length > 0) out.headers = headers;
      continue;
    }
    const value = form[field].trim();
    if (value !== '') out[JSON_NAMES[field]] = value;
  }
  return out;
}

function stableJson(value: JsonValue): string {
  if (Array.isArray(value)) return `[${value.map(stableJson).join(',')}]`;
  if (value !== null && typeof value === 'object') {
    return `{${Object.keys(value)
      .sort()
      .map((k) => `${JSON.stringify(k)}:${stableJson(value[k] ?? null)}`)
      .join(',')}}`;
  }
  return JSON.stringify(value);
}

/** Reports whether two settings objects are equal (key order ignored). */
export function sameConfig(a: JsonObject, b: JsonObject): boolean {
  return stableJson(a) === stableJson(b);
}

/**
 * Settings for UpdateChannel: undefined (keep the stored settings) when the
 * form still equals what the server returned, otherwise the full settings.
 */
export function configForUpdate(
  kind: ChannelKind,
  form: ChannelConfigForm,
  initial: JsonObject | undefined,
): JsonObject | undefined {
  const next = formToConfig(kind, form);
  const before = formToConfig(kind, configToForm(initial));
  return sameConfig(next, before) ? undefined : next;
}

export type ChannelConfigError =
  | 'required'
  | 'url'
  | 'botToken'
  | 'masked'
  | 'maskedDestination'
  | 'headerName'
  | 'headerReserved'
  | 'headerDuplicate'
  | 'headerLimit'
  | 'headerMasked'
  | 'headerMaskedDestination';

export type ChannelConfigErrors = Partial<Record<ChannelConfigField, ChannelConfigError>>;

function isHttpUrl(value: string): boolean {
  try {
    const url = new URL(value);
    return (url.protocol === 'http:' || url.protocol === 'https:') && url.host !== '';
  } catch {
    return false;
  }
}

function headerError(
  pairs: readonly KeyValuePair[],
  initial: ChannelConfigForm,
  destinationChanged: boolean,
): ChannelConfigError | undefined {
  const named = pairs.filter((p) => p.key.trim() !== '');
  if (named.length > MAX_CHANNEL_HEADERS) return 'headerLimit';
  const lower = named.map((p) => ({ key: p.key.trim().toLowerCase(), value: p.value }));
  if (duplicateKeys(lower).size > 0) return 'headerDuplicate';
  for (const { key, value } of named) {
    const name = key.trim();
    if (!HEADER_NAME_PATTERN.test(name)) return 'headerName';
    if (RESERVED_HEADERS.has(name.toLowerCase())) return 'headerReserved';
    if (isMaskedValue(value)) {
      const before = initial.headers.find((h) => h.key.toLowerCase() === name.toLowerCase());
      if (!before || before.value !== value) return 'headerMasked';
      // Credential headers are restored only while the URL is unchanged, so they
      // cannot be redirected to another server without knowing them.
      if (destinationChanged) return 'headerMaskedDestination';
    }
  }
  return undefined;
}

/** Telegram API base as the server compares it (trailing slashes dropped, default applied). */
export function normalizeTelegramBase(value: string): string {
  return value.trim().replace(/\/+$/, '') || DEFAULT_TELEGRAM_API_BASE;
}

/**
 * Reports whether the destination that masked credentials are bound to
 * changed: the URL of webhooks, the API base of Telegram bots.
 */
function destinationChanged(kind: ChannelKind, form: ChannelConfigForm, initial: ChannelConfigForm): boolean {
  if (kind === 'webhook') return form.url.trim() !== initial.url.trim();
  if (kind === 'telegram')
    return normalizeTelegramBase(form.apiBase) !== normalizeTelegramBase(initial.apiBase);
  return false;
}

/**
 * Validates form values for a kind. `initial` holds the values read from the
 * server (edit) so untouched masked values pass; a masked value that was
 * partially edited is rejected because the server cannot restore it.
 */
export function validateChannelConfig(
  kind: ChannelKind,
  form: ChannelConfigForm,
  initial: ChannelConfigForm = EMPTY_CHANNEL_CONFIG,
): ChannelConfigErrors {
  const errors: ChannelConfigErrors = {};
  const moved = destinationChanged(kind, form, initial);
  for (const field of CHANNEL_FIELDS[kind]) {
    if (field === 'headers') {
      const error = headerError(form.headers, initial, moved);
      if (error) errors.headers = error;
      continue;
    }
    const value = form[field].trim();
    if (value === '') {
      if (REQUIRED_FIELDS.has(field)) errors[field] = 'required';
      continue;
    }
    if (MASKABLE_FIELDS.has(field) && isMaskedValue(value)) {
      if (value !== initial[field].trim()) errors[field] = 'masked';
      // Bot tokens are restored only while the API base is unchanged.
      else if (field === 'botToken' && moved) errors[field] = 'maskedDestination';
      continue;
    }
    if ((field === 'url' || field === 'webhookUrl' || field === 'apiBase') && !isHttpUrl(value)) {
      errors[field] = 'url';
    } else if (field === 'botToken' && !BOT_TOKEN_PATTERN.test(value)) {
      errors[field] = 'botToken';
    }
  }
  return errors;
}

/** Severity rank for sorting and comparisons (unknown = 0). */
export function severityRank(severity: string): number {
  return (SEVERITIES as readonly string[]).indexOf(severity) + 1;
}
