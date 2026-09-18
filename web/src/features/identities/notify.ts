import { useQueryClient } from '@tanstack/react-query';
import { useCallback } from 'react';
import { toast } from 'sonner';

import { type BulkFailure, type BulkResult } from '@/gen/spinneret/v1/common_pb';
import { isKnownReason, type Translate } from '@/lib/errors';
import { type QueryDomain } from '@/lib/queryKeys';

import { summarizeBulkResult, type BulkSummary } from './operations';

/** Query domains whose data changes after identity or account operations. */
export const IDENTITY_MUTATION_DOMAINS: readonly QueryDomain[] = [
  'identities',
  'accounts',
  'heatmap',
  'dashboard',
  'identity-types',
];

/** Returns a callback invalidating every query affected by identity mutations. */
export function useInvalidateIdentityData(): () => void {
  const queryClient = useQueryClient();
  return useCallback(() => {
    for (const domain of IDENTITY_MUTATION_DOMAINS) {
      void queryClient.invalidateQueries({ queryKey: [domain] });
    }
  }, [queryClient]);
}

/** Translated reason of a bulk failure (known reasons), falling back to the raw code. */
export function failureReasonText(t: Translate, failure: Pick<BulkFailure, 'reason'>): string {
  if (!failure.reason) return '';
  return isKnownReason(failure.reason)
    ? t(`common:errors.reasons.${failure.reason}`, { seconds: 0 })
    : failure.reason;
}

/** Shows a toast summarizing a bulk result; returns the summary. */
export function notifyBulkResult(
  t: Translate,
  operationLabel: string,
  result: BulkResult | undefined,
): BulkSummary {
  const summary = summarizeBulkResult(result);
  const first = result?.failed[0];
  const firstReason = first ? [failureReasonText(t, first), first.message].filter(Boolean).join(': ') : '';
  switch (summary.outcome) {
    case 'success':
      toast.success(
        t('identities:result.toastSuccess', { operation: operationLabel, count: summary.succeeded }),
      );
      break;
    case 'partial':
      toast.warning(
        t('identities:result.toastPartial', {
          operation: operationLabel,
          succeeded: summary.succeeded,
          failed: summary.failed,
        }),
        { description: firstReason || undefined },
      );
      break;
    case 'failed':
      toast.error(t('identities:result.toastFailed', { operation: operationLabel, count: summary.failed }), {
        description: firstReason || undefined,
      });
      break;
    default:
      toast.info(t('identities:result.toastNone', { operation: operationLabel }));
  }
  return summary;
}
