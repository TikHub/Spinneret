import { type MessageInitShape } from '@bufbuild/protobuf';

import { type BulkResult } from '@/gen/spinneret/v1/common_pb';
import {
  type BulkOperateIdentitiesRequestSchema,
  type IdentityFilterSchema,
  type OperateIdentitiesRequestSchema,
} from '@/gen/spinneret/v1/identity_admin_pb';
import { parseDuration, PERMANENT } from '@/lib/duration';

/** Manual identity operations (OperateIdentitiesRequest.operation). */
export const IDENTITY_OPERATIONS = [
  'cooldown',
  'ban',
  'unban',
  'quarantine',
  'unquarantine',
  'expire',
  'disable',
  'enable',
  'archive',
  'restore',
  'activate',
  'reset_stats',
] as const;
export type IdentityOperation = (typeof IDENTITY_OPERATIONS)[number];

/** Operations that restrict scheduling (first group of the operations menu). */
export const RESTRICTING_OPERATIONS: readonly IdentityOperation[] = [
  'cooldown',
  'ban',
  'quarantine',
  'expire',
  'disable',
  'archive',
];
/** Operations that lift restrictions or reset state. */
export const RESTORING_OPERATIONS: readonly IdentityOperation[] = [
  'unban',
  'unquarantine',
  'enable',
  'restore',
  'activate',
  'reset_stats',
];

/** Level of cooldown and reset_stats ("" = server default). */
export type OperationScope = '' | 'identity_site' | 'identity_endpoint';

/** Maximum identity IDs per OperateIdentities call. */
export const MAX_OPERATE_IDS = 1000;
/** Maximum reason length (512). */
export const MAX_REASON_LENGTH = 512;

export interface OperationForm {
  operation: IdentityOperation;
  scope: OperationScope;
  endpointGroupId: string;
  duration: string;
  reason: string;
  resetFailures: boolean;
  resetHealth: boolean;
}

export type DurationRule = 'required' | 'optional' | 'none';

export interface OperationTraits {
  /** cooldown and ban need a positive duration; quarantine may use the policy default. */
  duration: DurationRule;
  allowPermanent: boolean;
  /** Which scope choices apply. */
  scope: 'cooldown' | 'reset_stats' | 'none';
  /** Offers reset_failures / reset_health. */
  resetFlags: boolean;
  /** Red confirm button. */
  destructive: boolean;
}

const RESET_FLAG_OPERATIONS: ReadonlySet<IdentityOperation> = new Set([
  'unban',
  'unquarantine',
  'enable',
  'restore',
  'activate',
]);
const DESTRUCTIVE_OPERATIONS: ReadonlySet<IdentityOperation> = new Set([
  'ban',
  'quarantine',
  'expire',
  'disable',
  'archive',
]);

export function operationTraits(operation: IdentityOperation): OperationTraits {
  return {
    duration:
      operation === 'cooldown' || operation === 'ban'
        ? 'required'
        : operation === 'quarantine'
          ? 'optional'
          : 'none',
    allowPermanent: operation === 'ban',
    scope: operation === 'cooldown' ? 'cooldown' : operation === 'reset_stats' ? 'reset_stats' : 'none',
    resetFlags: RESET_FLAG_OPERATIONS.has(operation),
    destructive: DESTRUCTIVE_OPERATIONS.has(operation),
  };
}

export function isIdentityOperation(value: string): value is IdentityOperation {
  return (IDENTITY_OPERATIONS as readonly string[]).includes(value);
}

/** Initial form for an operation. */
export function defaultOperationForm(operation: IdentityOperation): OperationForm {
  return {
    operation,
    scope: operation === 'cooldown' ? 'identity_site' : '',
    endpointGroupId: '',
    duration: operation === 'cooldown' ? '30m' : operation === 'ban' ? '7d' : '',
    reason: '',
    resetFailures: false,
    resetHealth: false,
  };
}

export type DurationError = 'required' | 'invalid' | 'permanentNotAllowed';

/** Validates a duration against its rule: required durations must be positive or "permanent". */
export function validateOperationDuration(
  value: string,
  rule: DurationRule,
  allowPermanent: boolean,
): DurationError | undefined {
  if (rule === 'none') return undefined;
  const trimmed = value.trim();
  if (trimmed === '') return rule === 'required' ? 'required' : undefined;
  const parsed = parseDuration(trimmed);
  if (!parsed) return 'invalid';
  if (parsed.permanent) return allowPermanent ? undefined : 'permanentNotAllowed';
  if (parsed.ms <= 0) return rule === 'required' ? 'required' : 'invalid';
  return undefined;
}

export interface OperationFormErrors {
  duration?: DurationError;
  endpointGroupId?: 'required';
  reason?: 'tooLong';
}

export function validateOperationForm(form: OperationForm): OperationFormErrors {
  const traits = operationTraits(form.operation);
  const errors: OperationFormErrors = {};
  const duration = validateOperationDuration(form.duration, traits.duration, traits.allowPermanent);
  if (duration) errors.duration = duration;
  if (traits.scope !== 'none' && form.scope === 'identity_endpoint' && !form.endpointGroupId) {
    errors.endpointGroupId = 'required';
  }
  if (form.reason.length > MAX_REASON_LENGTH) errors.reason = 'tooLong';
  return errors;
}

export function hasFormErrors(errors: object): boolean {
  return Object.values(errors).some((v) => v !== undefined);
}

/** True for a ban with the "permanent" duration. */
export function isPermanentBan(form: Pick<OperationForm, 'operation' | 'duration'>): boolean {
  return form.operation === 'ban' && parseDuration(form.duration.trim())?.permanent === true;
}

/** Irreversible operations need typed confirmation (archive, permanent ban). */
export function requiresTypedConfirmation(form: Pick<OperationForm, 'operation' | 'duration'>): boolean {
  return form.operation === 'archive' || isPermanentBan(form);
}

/** Fields shared by OperateIdentities and BulkOperateIdentities, normalized for the operation. */
export function operationRequestFields(form: OperationForm) {
  const traits = operationTraits(form.operation);
  const scope = traits.scope === 'none' ? '' : form.scope;
  const trimmedDuration = form.duration.trim();
  const duration =
    traits.duration === 'none' ? '' : parseDuration(trimmedDuration)?.permanent ? PERMANENT : trimmedDuration;
  return {
    operation: form.operation,
    scope,
    endpointGroupId: scope === 'identity_endpoint' ? form.endpointGroupId : '',
    duration,
    reason: form.reason.trim(),
    resetFailures: traits.resetFlags && form.resetFailures,
    resetHealth: traits.resetFlags && form.resetHealth,
  };
}

/** Builds an OperateIdentities request for explicit IDs (deduplicated, at most 1000). */
export function buildOperateIdentitiesRequest(
  ids: readonly string[],
  form: OperationForm,
): MessageInitShape<typeof OperateIdentitiesRequestSchema> {
  const unique = [...new Set(ids.filter(Boolean))];
  if (unique.length === 0) throw new Error('no identity IDs');
  if (unique.length > MAX_OPERATE_IDS) throw new Error(`at most ${MAX_OPERATE_IDS} identity IDs`);
  return { ids: unique, ...operationRequestFields(form) };
}

/** Builds a BulkOperateIdentities request for every identity matching the filter. */
export function buildBulkOperateRequest(
  namespace: string,
  filter: MessageInitShape<typeof IdentityFilterSchema>,
  form: OperationForm,
  dryRun: boolean,
): MessageInitShape<typeof BulkOperateIdentitiesRequestSchema> {
  return { namespace, filter, ...operationRequestFields(form), dryRun, limit: 0 };
}

/**
 * Source states of the lifecycle operations, mirroring the server transition
 * table (design doc 9.3); cooldown and reset_stats apply to any non-retired state.
 */
const LIFECYCLE_SOURCES: Partial<Record<IdentityOperation, readonly string[]>> = {
  ban: ['active', 'pending', 'quarantined', 'expired', 'disabled'],
  unban: ['banned'],
  quarantine: ['active', 'pending'],
  unquarantine: ['quarantined'],
  expire: ['active', 'pending', 'quarantined'],
  disable: ['pending', 'active', 'expired', 'banned', 'quarantined'],
  enable: ['disabled'],
  archive: ['pending', 'active', 'expired', 'banned', 'quarantined', 'disabled'],
  restore: ['retired'],
  activate: ['pending'],
};

/** Operations offered for a single identity in a given state (the server rejects the others). */
export function availableOperations(state: string): IdentityOperation[] {
  return IDENTITY_OPERATIONS.filter((op) => {
    const sources = LIFECYCLE_SOURCES[op];
    return sources ? sources.includes(state) : state !== 'retired';
  });
}

export type BulkOutcome = 'success' | 'partial' | 'failed' | 'none';

export interface BulkSummary {
  matched: number;
  succeeded: number;
  failed: number;
  outcome: BulkOutcome;
}

/** Summarizes a BulkResult for toasts and the result dialog. */
export function summarizeBulkResult(result: BulkResult | undefined): BulkSummary {
  const succeeded = result?.succeeded ?? 0;
  const failed = result?.failed.length ?? 0;
  const matched = Math.max(result?.matched ?? 0, succeeded + failed);
  let outcome: BulkOutcome = 'none';
  if (failed > 0 && succeeded > 0) outcome = 'partial';
  else if (failed > 0) outcome = 'failed';
  else if (succeeded > 0) outcome = 'success';
  return { matched, succeeded, failed, outcome };
}
