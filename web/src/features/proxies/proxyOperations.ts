import { type BulkResult } from '@/gen/spinneret/v1/common_pb';
import { DURATION_PATTERN, parseDuration, PERMANENT } from '@/lib/duration';

/** Manual operations of OperateProxiesRequest.operation. */
export const PROXY_OPERATIONS = [
  'disable',
  'enable',
  'ban',
  'unban',
  'cooldown',
  'quarantine',
  'unquarantine',
  'archive',
  'restore',
  'reset_stats',
] as const;
export type ProxyOperation = (typeof PROXY_OPERATIONS)[number];

/** Pseudo operation for DeleteProxies (handled by its own RPC). */
export const DELETE_ACTION = 'delete';
export type ProxyBulkAction = ProxyOperation | typeof DELETE_ACTION;

/** Maximum IDs per OperateProxies/DeleteProxies request. */
export const MAX_BULK_IDS = 1000;
/** Maximum length of the operator reason. */
export const MAX_REASON_LENGTH = 512;

export interface OperationSpec {
  /** Whether the operation takes a duration. */
  duration: 'none' | 'required' | 'optional';
  /** "permanent" is accepted (ban only). */
  allowPermanent: boolean;
  /** The operation can be limited to one site. */
  siteScoped: boolean;
  /** Red confirm button. */
  destructive: boolean;
}

/** Operation parameters (mirrors OperateProxiesRequest validation and the proxy service). */
export const OPERATION_SPECS: Readonly<Record<ProxyOperation, OperationSpec>> = {
  disable: { duration: 'none', allowPermanent: false, siteScoped: false, destructive: true },
  enable: { duration: 'none', allowPermanent: false, siteScoped: false, destructive: false },
  ban: { duration: 'required', allowPermanent: true, siteScoped: false, destructive: true },
  unban: { duration: 'none', allowPermanent: false, siteScoped: false, destructive: false },
  cooldown: { duration: 'required', allowPermanent: false, siteScoped: true, destructive: false },
  quarantine: { duration: 'optional', allowPermanent: false, siteScoped: false, destructive: true },
  unquarantine: { duration: 'none', allowPermanent: false, siteScoped: false, destructive: false },
  archive: { duration: 'none', allowPermanent: false, siteScoped: false, destructive: true },
  restore: { duration: 'none', allowPermanent: false, siteScoped: false, destructive: false },
  reset_stats: { duration: 'none', allowPermanent: false, siteScoped: true, destructive: false },
};

/** Operation dialog inputs. */
export interface OperationForm {
  /** Site name for site-scoped operations; empty = global. */
  site: string;
  duration: string;
  reason: string;
}

export const EMPTY_OPERATION_FORM: OperationForm = { site: '', duration: '', reason: '' };

export type OperationFormError =
  | 'no_ids'
  | 'too_many_ids'
  | 'duration_required'
  | 'duration_invalid'
  | 'duration_zero'
  | 'duration_permanent'
  | 'reason_too_long';

/** Plain init object of OperateProxiesRequest. */
export interface OperateProxiesInit {
  ids: string[];
  operation: ProxyOperation;
  site: string;
  duration: string;
  reason: string;
}

export function isProxyOperation(value: string): value is ProxyOperation {
  return (PROXY_OPERATIONS as readonly string[]).includes(value);
}

/** Deduplicates IDs, keeping the first occurrence order and dropping empty values. */
export function uniqueIds(ids: readonly string[]): string[] {
  return [...new Set(ids.filter((id) => id !== ''))];
}

/** Validates the dialog inputs of an operation; returns every problem found. */
export function validateOperation(
  operation: ProxyOperation,
  ids: readonly string[],
  form: OperationForm,
): OperationFormError[] {
  const errors: OperationFormError[] = [];
  const unique = uniqueIds(ids);
  if (unique.length === 0) errors.push('no_ids');
  if (unique.length > MAX_BULK_IDS) errors.push('too_many_ids');

  const spec = OPERATION_SPECS[operation];
  const duration = form.duration.trim();
  if (spec.duration !== 'none') {
    if (duration === '') {
      if (spec.duration === 'required') errors.push('duration_required');
    } else {
      const parsed = parseDuration(duration);
      if (!parsed || !DURATION_PATTERN.test(parsed.permanent ? PERMANENT : duration)) {
        errors.push('duration_invalid');
      } else if (parsed.permanent && !spec.allowPermanent) {
        errors.push('duration_permanent');
      } else if (!parsed.permanent && parsed.ms <= 0 && spec.duration === 'required') {
        errors.push('duration_zero');
      }
    }
  }
  if (form.reason.trim().length > MAX_REASON_LENGTH) errors.push('reason_too_long');
  return errors;
}

/** True when the operation cannot be undone easily and needs typed confirmation. */
export function requiresTypedConfirmation(operation: ProxyOperation, form: OperationForm): boolean {
  if (operation === 'archive') return true;
  return operation === 'ban' && parseDuration(form.duration)?.permanent === true;
}

/**
 * Builds the OperateProxies request: fields the operation does not use are
 * cleared, the duration is trimmed ("permanent" normalized to lower case) and
 * the reason trimmed.
 */
export function buildOperateRequest(
  operation: ProxyOperation,
  ids: readonly string[],
  form: OperationForm,
): OperateProxiesInit {
  const spec = OPERATION_SPECS[operation];
  const rawDuration = form.duration.trim();
  const parsed = rawDuration === '' ? undefined : parseDuration(rawDuration);
  const duration = spec.duration === 'none' ? '' : parsed?.permanent ? PERMANENT : rawDuration;
  return {
    ids: uniqueIds(ids),
    operation,
    site: spec.siteScoped ? form.site.trim() : '',
    duration,
    reason: form.reason.trim(),
  };
}

/**
 * Operations that make sense for a proxy in the given state (order = menu order),
 * following the transitions of internal/proxy/operate.go: enable revives disabled
 * and dead proxies, quarantine rejects banned ones, and a retired proxy must be
 * restored before anything else changes its state.
 */
export function operationsForState(state: string): ProxyOperation[] {
  if (state === 'retired') return ['restore', 'reset_stats'];
  const ops: ProxyOperation[] = [];
  if (state === 'disabled' || state === 'dead') ops.push('enable');
  if (state !== 'disabled') ops.push('disable');
  ops.push('cooldown', state === 'banned' ? 'unban' : 'ban');
  if (state === 'quarantined') ops.push('unquarantine');
  else if (state !== 'banned') ops.push('quarantine');
  ops.push('reset_stats', 'archive');
  return ops;
}

/** Counts of a BulkResult as plain numbers. */
export interface BulkSummary {
  matched: number;
  succeeded: number;
  failed: number;
}

export function summarizeBulkResult(result: BulkResult | undefined, requested: number): BulkSummary {
  if (!result) return { matched: requested, succeeded: requested, failed: 0 };
  return { matched: result.matched, succeeded: result.succeeded, failed: result.failed.length };
}
