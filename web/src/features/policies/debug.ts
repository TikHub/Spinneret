import { type MessageInitShape } from '@bufbuild/protobuf';

import { type CounterRequirement, type DebugReportRequestSchema } from '@/gen/spinneret/v1/policy_admin_pb';
import { parseDuration } from '@/lib/duration';

import { COUNTER_SUBJECTS, OUTCOMES, type CounterSubject } from './constants';
import { newKey } from './model/common';

/** Counter keys accepted by DebugReport: "<subject>:<outcome>:<window>". */
export const COUNTER_KEY_PATTERN = /^(identity|account|proxy):[a-z_]{1,32}:[0-9a-z.]{1,16}$/;
/** Ban count windows accepted by DebugReport. */
export const WINDOW_KEY_PATTERN = /^[0-9a-z.]{1,16}$/;
export const MAX_COUNTS = 256;
export const MAX_BAN_COUNTS = 32;

export interface CountEntry {
  key: string;
  subject: CounterSubject;
  outcome: string;
  window: string;
  value: string;
}

export interface BanCountEntry {
  key: string;
  window: string;
  value: string;
}

export type DebugTargetMode = 'endpoint_group' | 'uri' | 'report_uri';

export interface DebugReportFields {
  uri: string;
  method: string;
  httpStatus: string;
  businessCode: string;
  errorKind: string;
  markers: string[];
  latencyMs: string;
  responseBytes: string;
  outcomeHint: string;
}

export interface DebugContextFields {
  identityState: string;
  hasAccount: boolean;
  hasProxy: boolean;
  endpointScore: string;
  endpointSamples: string;
  globalScore: string;
  globalSamples: string;
  endpointStreak: string;
  siteStreak: string;
  proxyStreak: string;
  endpointCooldownRemaining: string;
}

export interface DebugForm {
  site: string;
  client: string;
  targetMode: DebugTargetMode;
  endpointGroup: string;
  uri: string;
  useDraft: boolean;
  report: DebugReportFields;
  context: DebugContextFields;
  counts: CountEntry[];
  banCounts: BanCountEntry[];
}

export function emptyDebugForm(): DebugForm {
  return {
    site: '',
    client: '',
    targetMode: 'endpoint_group',
    endpointGroup: '',
    uri: '',
    useDraft: false,
    report: {
      uri: '',
      method: 'GET',
      httpStatus: '200',
      businessCode: '',
      errorKind: '',
      markers: [],
      latencyMs: '',
      responseBytes: '',
      outcomeHint: '',
    },
    context: {
      identityState: 'active',
      hasAccount: false,
      hasProxy: true,
      endpointScore: '70',
      endpointSamples: '0',
      globalScore: '70',
      globalSamples: '0',
      endpointStreak: '0',
      siteStreak: '0',
      proxyStreak: '0',
      endpointCooldownRemaining: '',
    },
    counts: [],
    banCounts: [],
  };
}

/** Builds a counter key "<subject>:<outcome>:<window>" (window trimmed and lower-cased). */
export function counterKey(subject: string, outcome: string, window: string): string {
  return `${subject.trim()}:${outcome.trim()}:${window.trim().toLowerCase()}`;
}

/** Splits a counter key; undefined when it does not follow the pattern. */
export function parseCounterKey(
  key: string,
): { subject: CounterSubject; outcome: string; window: string } | undefined {
  if (!COUNTER_KEY_PATTERN.test(key)) return undefined;
  const [subject, outcome, window] = key.split(':');
  if (!subject || !outcome || !window) return undefined;
  return { subject: subject as CounterSubject, outcome, window };
}

/** Count entries (value 1) for the counters an action policy needs, skipping keys already present. */
export function countEntriesFor(
  requirements: readonly CounterRequirement[],
  existing: readonly CountEntry[],
): CountEntry[] {
  const present = new Set(existing.map((e) => counterKey(e.subject, e.outcome, e.window)));
  const added: CountEntry[] = [];
  for (const r of requirements) {
    if (!(COUNTER_SUBJECTS as readonly string[]).includes(r.subject)) continue;
    const key = counterKey(r.subject, r.outcome, r.window);
    if (present.has(key)) continue;
    present.add(key);
    added.push({
      key: newKey(),
      subject: r.subject as CounterSubject,
      outcome: r.outcome,
      window: r.window,
      value: '1',
    });
  }
  return added;
}

const NON_NEGATIVE_INT = /^\d+$/;

function isWindow(value: string): boolean {
  const trimmed = value.trim().toLowerCase();
  const parsed = parseDuration(trimmed);
  return WINDOW_KEY_PATTERN.test(trimmed) && parsed !== undefined && !parsed.permanent && parsed.ms > 0;
}

export type CountError = 'key' | 'value' | 'duplicate';

/** Validates count entries; returns the map for the request and per-entry errors (by entry key). */
export function buildCounts(entries: readonly CountEntry[]): {
  counts: Record<string, bigint>;
  errors: Record<string, CountError>;
} {
  const counts: Record<string, bigint> = {};
  const errors: Record<string, CountError> = {};
  for (const entry of entries) {
    const key = counterKey(entry.subject, entry.outcome, entry.window);
    if (
      !COUNTER_KEY_PATTERN.test(key) ||
      !(OUTCOMES as readonly string[]).includes(entry.outcome) ||
      !isWindow(entry.window)
    ) {
      errors[entry.key] = 'key';
    } else if (!NON_NEGATIVE_INT.test(entry.value.trim())) {
      errors[entry.key] = 'value';
    } else if (Object.prototype.hasOwnProperty.call(counts, key)) {
      errors[entry.key] = 'duplicate';
    } else {
      counts[key] = BigInt(entry.value.trim());
    }
  }
  return { counts, errors };
}

/** Validates ban count entries keyed by window. */
export function buildBanCounts(entries: readonly BanCountEntry[]): {
  banCounts: Record<string, bigint>;
  errors: Record<string, CountError>;
} {
  const banCounts: Record<string, bigint> = {};
  const errors: Record<string, CountError> = {};
  for (const entry of entries) {
    const window = entry.window.trim().toLowerCase();
    if (!isWindow(window)) errors[entry.key] = 'key';
    else if (!NON_NEGATIVE_INT.test(entry.value.trim())) errors[entry.key] = 'value';
    else if (Object.prototype.hasOwnProperty.call(banCounts, window)) errors[entry.key] = 'duplicate';
    else banCounts[window] = BigInt(entry.value.trim());
  }
  return { banCounts, errors };
}

export type DebugFieldError = 'required' | 'httpStatus' | 'integer' | 'score' | 'duration' | 'tooMany';

export type DebugRequestInit = MessageInitShape<typeof DebugReportRequestSchema>;

function intOrZero(value: string): number {
  const trimmed = value.trim();
  return trimmed === '' ? 0 : Number(trimmed);
}

function checkInt(value: string, max = Number.MAX_SAFE_INTEGER): boolean {
  const trimmed = value.trim();
  return trimmed === '' || (NON_NEGATIVE_INT.test(trimmed) && Number(trimmed) <= max);
}

function checkScore(value: string): boolean {
  const trimmed = value.trim();
  if (trimmed === '') return true;
  const n = Number(trimmed);
  return Number.isFinite(n) && n >= 0 && n <= 100;
}

/** Field errors of the debug form (by field path such as "report.httpStatus"). */
export function validateDebugForm(form: DebugForm): Record<string, DebugFieldError> {
  const errors: Record<string, DebugFieldError> = {};
  if (!form.site) errors.site = 'required';
  if (!form.client) errors.client = 'required';
  if (form.targetMode === 'endpoint_group' && !form.endpointGroup) errors.endpointGroup = 'required';
  if (form.targetMode === 'uri' && !form.uri.trim()) errors.uri = 'required';
  if (form.targetMode === 'report_uri' && !form.report.uri.trim()) errors['report.uri'] = 'required';
  if (!checkInt(form.report.httpStatus, 999)) errors['report.httpStatus'] = 'httpStatus';
  if (!checkInt(form.report.latencyMs, 2_147_483_647)) errors['report.latencyMs'] = 'integer';
  if (!checkInt(form.report.responseBytes)) errors['report.responseBytes'] = 'integer';
  if (form.report.markers.length > 32) errors['report.markers'] = 'tooMany';
  const c = form.context;
  if (!checkScore(c.endpointScore)) errors['context.endpointScore'] = 'score';
  if (!checkScore(c.globalScore)) errors['context.globalScore'] = 'score';
  for (const field of ['endpointSamples', 'globalSamples'] as const) {
    if (!checkInt(c[field], 2_147_483_647)) errors[`context.${field}`] = 'integer';
  }
  for (const field of ['endpointStreak', 'siteStreak', 'proxyStreak'] as const) {
    if (!checkInt(c[field], 1_000_000)) errors[`context.${field}`] = 'integer';
  }
  const remaining = c.endpointCooldownRemaining.trim();
  if (remaining !== '') {
    const parsed = parseDuration(remaining);
    if (!parsed || parsed.permanent) errors['context.endpointCooldownRemaining'] = 'duration';
  }
  if (form.counts.length > MAX_COUNTS) errors.counts = 'tooMany';
  if (form.banCounts.length > MAX_BAN_COUNTS) errors.banCounts = 'tooMany';
  return errors;
}

export type BuildDebugResult =
  | { ok: true; request: DebugRequestInit }
  | {
      ok: false;
      fieldErrors: Record<string, DebugFieldError>;
      countErrors: Record<string, CountError>;
      banCountErrors: Record<string, CountError>;
    };

/** Validates the form and builds the DebugReport request. */
export function buildDebugRequest(
  form: DebugForm,
  namespace: string,
  draftPolicyId?: string,
): BuildDebugResult {
  const fieldErrors = validateDebugForm(form);
  const { counts, errors: countErrors } = buildCounts(form.counts);
  const { banCounts, errors: banCountErrors } = buildBanCounts(form.banCounts);
  if (
    Object.keys(fieldErrors).length > 0 ||
    Object.keys(countErrors).length > 0 ||
    Object.keys(banCountErrors).length > 0
  ) {
    return { ok: false, fieldErrors, countErrors, banCountErrors };
  }
  const r = form.report;
  const c = form.context;
  const target: DebugRequestInit['target'] =
    form.targetMode === 'endpoint_group'
      ? { case: 'endpointGroup', value: form.endpointGroup }
      : form.targetMode === 'uri'
        ? { case: 'uri', value: form.uri.trim() }
        : { case: undefined };
  return {
    ok: true,
    request: {
      namespace,
      site: form.site,
      client: form.client,
      target,
      report: {
        uri: r.uri.trim(),
        method: r.method.trim().toUpperCase(),
        httpStatus: intOrZero(r.httpStatus),
        businessCode: r.businessCode.trim(),
        errorKind: r.errorKind,
        markers: r.markers,
        outcomeHint: r.outcomeHint,
        latencyMs: intOrZero(r.latencyMs),
        responseBytes: BigInt(intOrZero(r.responseBytes)),
      },
      identityState: c.identityState === 'active' ? '' : c.identityState,
      counts,
      hasAccount: c.hasAccount,
      hasProxy: c.hasProxy,
      endpointScore: Number(c.endpointScore.trim() || '0'),
      endpointSamples: intOrZero(c.endpointSamples),
      globalScore: Number(c.globalScore.trim() || '0'),
      globalSamples: intOrZero(c.globalSamples),
      endpointStreak: intOrZero(c.endpointStreak),
      siteStreak: intOrZero(c.siteStreak),
      proxyStreak: intOrZero(c.proxyStreak),
      banCounts,
      draftPolicyId: form.useDraft && draftPolicyId ? draftPolicyId : '',
      endpointCooldownRemaining: c.endpointCooldownRemaining.trim(),
    },
  };
}

function pick(obj: Record<string, unknown>, snake: string, camel: string): unknown {
  return obj[snake] ?? obj[camel];
}

function asText(value: unknown): string | undefined {
  if (value === undefined || value === null) return undefined;
  if (typeof value === 'string') return value;
  if (typeof value === 'number' || typeof value === 'bigint') return String(value);
  return undefined;
}

/**
 * Fills report fields from a pasted report (JSON as sent to ReportService, in
 * snake_case or camelCase; a {"reports": [...]} batch uses its first report).
 * Returns undefined when the text is not a JSON object.
 */
export function reportFieldsFromJson(
  text: string,
  current: DebugReportFields,
): DebugReportFields | undefined {
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch {
    return undefined;
  }
  if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) return undefined;
  let obj = parsed as Record<string, unknown>;
  const batch = obj.reports;
  if (Array.isArray(batch)) {
    const first: unknown = batch[0];
    if (typeof first !== 'object' || first === null) return undefined;
    obj = first as Record<string, unknown>;
  }
  const markers = obj.markers;
  return {
    uri: asText(obj.uri) ?? current.uri,
    method: asText(obj.method) ?? current.method,
    httpStatus: asText(pick(obj, 'http_status', 'httpStatus')) ?? '0',
    businessCode: asText(pick(obj, 'business_code', 'businessCode')) ?? '',
    errorKind: asText(pick(obj, 'error_kind', 'errorKind')) ?? '',
    markers: Array.isArray(markers) ? markers.map((m) => asText(m) ?? '').filter(Boolean) : [],
    latencyMs: asText(pick(obj, 'latency_ms', 'latencyMs')) ?? '',
    responseBytes: asText(pick(obj, 'response_bytes', 'responseBytes')) ?? '',
    outcomeHint: asText(pick(obj, 'outcome_hint', 'outcomeHint')) ?? '',
  };
}
