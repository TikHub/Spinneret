import { type MessageInitShape } from '@bufbuild/protobuf';
import { timestampFromMs } from '@bufbuild/protobuf/wkt';

import { type RevertActionsRequestSchema } from '@/gen/spinneret/v1/identity_admin_pb';

import {
  fromDateTimeLocalValue,
  toDateTimeLocalValue,
  validateTimeRange,
  type TimeRangeError,
} from './timeRange';

/** Automatic actions RevertActions can undo. */
export const REVERTIBLE_ACTIONS = ['ban', 'quarantine', 'expire', 'cooldown'] as const;
export type RevertibleAction = (typeof REVERTIBLE_ACTIONS)[number];

export interface RevertForm {
  site: string;
  policyId: string;
  rule: string;
  /** Empty = every revertible action. */
  actions: RevertibleAction[];
  /** datetime-local values. */
  start: string;
  end: string;
  resetFailures: boolean;
  resetHealth: boolean;
}

/** Default form: the last hour, every action. */
export function defaultRevertForm(now: number, site = ''): RevertForm {
  return {
    site,
    policyId: '',
    rule: '',
    actions: [],
    start: toDateTimeLocalValue(now - 3_600_000),
    end: '',
    resetFailures: false,
    resetHealth: false,
  };
}

export interface RevertFormErrors {
  timeRange?: TimeRangeError;
  rule?: 'tooLong';
}

export function validateRevertForm(form: RevertForm): RevertFormErrors {
  const errors: RevertFormErrors = {};
  const range = validateTimeRange(form.start, form.end);
  if (range) errors.timeRange = range;
  if (form.rule.trim().length > 128) errors.rule = 'tooLong';
  return errors;
}

/** Builds a RevertActions request; throws when the start time is missing or invalid. */
export function buildRevertRequest(
  namespace: string,
  form: RevertForm,
  dryRun: boolean,
): MessageInitShape<typeof RevertActionsRequestSchema> {
  const start = fromDateTimeLocalValue(form.start);
  if (start === undefined) throw new Error('time range start is required');
  const end = form.end.trim() === '' ? undefined : fromDateTimeLocalValue(form.end);
  return {
    namespace,
    site: form.site,
    policyId: form.policyId.trim(),
    rule: form.rule.trim(),
    actions: REVERTIBLE_ACTIONS.filter((a) => form.actions.includes(a)),
    timeRange: {
      start: timestampFromMs(start),
      end: end === undefined ? undefined : timestampFromMs(end),
    },
    resetFailures: form.resetFailures,
    resetHealth: form.resetHealth,
    dryRun,
  };
}
