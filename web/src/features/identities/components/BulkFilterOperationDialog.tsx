import { useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import { useAuth } from '@/app/auth/AuthContext';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { type BulkResult } from '@/gen/spinneret/v1/common_pb';
import { identityClient } from '@/lib/clients';
import { formatNumber } from '@/lib/format';

import { toIdentityFilter, type IdentityListParams } from '../identitySearch';
import {
  buildBulkOperateRequest,
  defaultOperationForm,
  hasFormErrors,
  operationTraits,
  validateOperationForm,
  type IdentityOperation,
  type OperationForm,
} from '../operations';
import { FilterSummary } from './FilterSummary';
import { OperationFields } from './OperationFields';

export interface BulkFilterOperationDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  operation: IdentityOperation;
  /** Current list filter; the operation applies to every identity matching it. */
  params: IdentityListParams;
  onApplied: (result: BulkResult | undefined, form: OperationForm) => void;
}

/**
 * "Apply to all matching the filter" (BulkOperateIdentities): the form step
 * runs a dry run to count matching identities, the second step applies the
 * operation after typed confirmation.
 */
export function BulkFilterOperationDialog({
  open,
  onOpenChange,
  operation,
  params,
  onApplied,
}: BulkFilterOperationDialogProps) {
  const { t, i18n } = useTranslation('identities');
  const { namespaceName } = useAuth();
  const [form, setForm] = useState<OperationForm>(() => defaultOperationForm(operation));
  const [step, setStep] = useState<'form' | 'confirm'>('form');
  const [matched, setMatched] = useState(0);
  // Set while the form step hands over to the confirm step, so its closing does not end the flow.
  const advancing = useRef(false);
  const errors = validateOperationForm(form);
  const traits = operationTraits(operation);
  const label = t(`operations.${operation}`);
  const namespace = namespaceName ?? '';
  const filter = toIdentityFilter(params);

  const preview = async () => {
    const res = await identityClient.bulkOperateIdentities(
      buildBulkOperateRequest(namespace, filter, form, true),
    );
    const count = res.result?.matched ?? 0;
    if (count === 0) {
      toast.info(t('bulk.noMatches'));
      return;
    }
    setMatched(count);
    advancing.current = true;
    setStep('confirm');
  };

  const apply = async () => {
    const res = await identityClient.bulkOperateIdentities(
      buildBulkOperateRequest(namespace, filter, form, false),
    );
    onApplied(res.result, form);
  };

  return (
    <>
      <ConfirmDialog
        open={open && step === 'form'}
        onOpenChange={(next) => {
          if (!next && advancing.current) {
            advancing.current = false;
            return;
          }
          onOpenChange(next);
        }}
        title={t('bulk.title', { operation: label })}
        description={t('bulk.description')}
        confirmLabel={t('bulk.preview')}
        confirmDisabled={hasFormErrors(errors) || !namespace}
        onConfirm={preview}
      >
        <div className="grid gap-4">
          <FilterSummary params={params} />
          <p className="text-sm text-muted-foreground">{t(`operationHints.${operation}`)}</p>
          <OperationFields form={form} onChange={setForm} errors={errors} site={params.site || undefined} />
        </div>
      </ConfirmDialog>
      <ConfirmDialog
        open={open && step === 'confirm'}
        onOpenChange={(next) => {
          if (!next) setStep('form');
          onOpenChange(next);
        }}
        title={t('bulk.confirmTitle', {
          operation: label,
          count: matched,
          formatted: formatNumber(matched, undefined, i18n.language),
        })}
        description={t('bulk.confirmDescription')}
        destructive={traits.destructive}
        affectedCount={matched}
        confirmText={operation}
        confirmLabel={t('bulk.apply', { operation: label })}
        onConfirm={apply}
      >
        <FilterSummary params={params} />
      </ConfirmDialog>
    </>
  );
}
