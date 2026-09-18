import { type MessageInitShape } from '@bufbuild/protobuf';

import {
  MAX_REASON_LENGTH,
  validateOperationDuration,
  type DurationError,
} from '@/features/identities/operations';
import {
  type Account,
  type OperateAccountRequestSchema,
  type UpsertAccountRequestSchema,
} from '@/gen/spinneret/v1/identity_admin_pb';
import type { PageSearch } from '@/router';
import { parseDuration, PERMANENT } from '@/lib/duration';

/** Account states (ListAccountsRequest.state). */
export const ACCOUNT_STATES = ['active', 'banned', 'disabled'] as const;
export type AccountState = (typeof ACCOUNT_STATES)[number];

/** Account operations (OperateAccountRequest.operation). */
export const ACCOUNT_OPERATIONS = ['ban', 'unban', 'cooldown', 'disable', 'enable'] as const;
export type AccountOperation = (typeof ACCOUNT_OPERATIONS)[number];

export interface AccountListParams {
  site: string;
  state: AccountState | '';
  search: string;
}

function readText(value: unknown, max: number): string {
  if (typeof value === 'number' && Number.isFinite(value)) return String(value).slice(0, max);
  return typeof value === 'string' ? value.trim().slice(0, max) : '';
}

/** Parses the accounts page search params (site, state, search). */
export function parseAccountSearch(search: PageSearch): AccountListParams {
  const state = readText(search.state, 16);
  return {
    site: readText(search.site, 64),
    state: (ACCOUNT_STATES as readonly string[]).includes(state) ? (state as AccountState) : '',
    search: readText(search.search, 256),
  };
}

/** Search params for list parameters (defaults omitted, other keys kept). */
export function mergeAccountSearch(prev: PageSearch, params: AccountListParams): PageSearch {
  return {
    ...prev,
    site: params.site || undefined,
    state: params.state || undefined,
    search: params.search || undefined,
  };
}

export interface AccountOperationForm {
  operation: AccountOperation;
  duration: string;
  reason: string;
}

export function defaultAccountOperationForm(operation: AccountOperation): AccountOperationForm {
  return {
    operation,
    duration: operation === 'ban' ? '7d' : operation === 'cooldown' ? '30m' : '',
    reason: '',
  };
}

export function accountOperationTraits(operation: AccountOperation) {
  return {
    needsDuration: operation === 'ban' || operation === 'cooldown',
    allowPermanent: operation === 'ban',
    destructive: operation === 'ban' || operation === 'disable',
  };
}

export interface AccountOperationErrors {
  duration?: DurationError;
  reason?: 'tooLong';
}

export function validateAccountOperation(form: AccountOperationForm): AccountOperationErrors {
  const traits = accountOperationTraits(form.operation);
  const errors: AccountOperationErrors = {};
  const duration = validateOperationDuration(
    form.duration,
    traits.needsDuration ? 'required' : 'none',
    traits.allowPermanent,
  );
  if (duration) errors.duration = duration;
  if (form.reason.length > MAX_REASON_LENGTH) errors.reason = 'tooLong';
  return errors;
}

/** Permanent bans need typed confirmation. */
export function isPermanentAccountBan(form: AccountOperationForm): boolean {
  return form.operation === 'ban' && parseDuration(form.duration.trim())?.permanent === true;
}

export function buildOperateAccountRequest(
  id: string,
  form: AccountOperationForm,
): MessageInitShape<typeof OperateAccountRequestSchema> {
  const traits = accountOperationTraits(form.operation);
  const trimmed = form.duration.trim();
  const duration = !traits.needsDuration ? '' : parseDuration(trimmed)?.permanent ? PERMANENT : trimmed;
  return { id, operation: form.operation, duration, reason: form.reason.trim() };
}

/**
 * Operations offered for an account in a given state, mirroring the server:
 * ban from active, disabled or banned (changes the ban end), unban from banned,
 * disable from active only, enable from disabled; cooldown is offered for active accounts.
 */
export function availableAccountOperations(state: string): AccountOperation[] {
  if (state === 'banned') return ['unban', 'ban'];
  if (state === 'disabled') return ['enable', 'ban'];
  return ['cooldown', 'ban', 'disable'];
}

export const ACCOUNT_LIMITS = { externalRef: 256, region: 64, tags: 64, tagLength: 64, notes: 4096 } as const;

export interface UpsertAccountForm {
  site: string;
  externalRef: string;
  region: string;
  tags: string[];
  notes: string;
}

export function upsertFormFromAccount(account: Account | undefined, site = ''): UpsertAccountForm {
  return {
    site: account?.site ?? site,
    externalRef: account?.externalRef ?? '',
    region: account?.region ?? '',
    tags: [...(account?.tags ?? [])],
    notes: account?.notes ?? '',
  };
}

export interface UpsertAccountErrors {
  site?: 'required';
  externalRef?: 'required' | 'tooLong';
  region?: 'tooLong';
  tags?: 'tooMany';
  notes?: 'tooLong';
}

export function validateUpsertAccount(form: UpsertAccountForm): UpsertAccountErrors {
  const errors: UpsertAccountErrors = {};
  if (!form.site) errors.site = 'required';
  const ref = form.externalRef.trim();
  if (!ref) errors.externalRef = 'required';
  else if (ref.length > ACCOUNT_LIMITS.externalRef) errors.externalRef = 'tooLong';
  if (form.region.trim().length > ACCOUNT_LIMITS.region) errors.region = 'tooLong';
  if (form.tags.length > ACCOUNT_LIMITS.tags) errors.tags = 'tooMany';
  if (form.notes.length > ACCOUNT_LIMITS.notes) errors.notes = 'tooLong';
  return errors;
}

export function buildUpsertAccountRequest(
  namespace: string,
  form: UpsertAccountForm,
): MessageInitShape<typeof UpsertAccountRequestSchema> {
  return {
    namespace,
    site: form.site,
    externalRef: form.externalRef.trim(),
    region: form.region.trim(),
    tags: form.tags.map((tag) => tag.trim()).filter(Boolean),
    notes: form.notes,
  };
}
