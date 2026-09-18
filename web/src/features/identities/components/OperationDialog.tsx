import { useState } from 'react';
import { useTranslation } from 'react-i18next';

import { ConfirmDialog } from '@/components/ConfirmDialog';
import { type BulkResult } from '@/gen/spinneret/v1/common_pb';
import { identityClient } from '@/lib/clients';

import {
  buildOperateIdentitiesRequest,
  defaultOperationForm,
  hasFormErrors,
  operationTraits,
  requiresTypedConfirmation,
  validateOperationForm,
  type IdentityOperation,
  type OperationForm,
} from '../operations';
import { OperationFields } from './OperationFields';

export interface OperationDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  operation: IdentityOperation;
  /** Identity IDs to operate on (1..1000). */
  ids: readonly string[];
  /** Shared site of the identities, when known. */
  site?: string;
  /** Called after the server applied the operation. */
  onApplied: (result: BulkResult | undefined, form: OperationForm) => void;
}

/**
 * Confirmation dialog for one manual operation on explicit identities
 * (IdentityAdminService.OperateIdentities). Mount it with a key per opening so
 * the form starts fresh.
 */
export function OperationDialog({
  open,
  onOpenChange,
  operation,
  ids,
  site,
  onApplied,
}: OperationDialogProps) {
  const { t } = useTranslation('identities');
  const [form, setForm] = useState<OperationForm>(() => defaultOperationForm(operation));
  const errors = validateOperationForm(form);
  const traits = operationTraits(operation);
  const label = t(`operations.${operation}`);

  return (
    <ConfirmDialog
      open={open}
      onOpenChange={onOpenChange}
      title={t('operation.title', { operation: label, count: ids.length })}
      description={t(`operationHints.${operation}`)}
      destructive={traits.destructive}
      affectedCount={ids.length}
      confirmText={requiresTypedConfirmation(form) ? operation : undefined}
      confirmLabel={label}
      confirmDisabled={hasFormErrors(errors) || ids.length === 0}
      onConfirm={async () => {
        const res = await identityClient.operateIdentities(buildOperateIdentitiesRequest(ids, form));
        onApplied(res.result, form);
      }}
    >
      <OperationFields form={form} onChange={setForm} errors={errors} site={site} />
    </ConfirmDialog>
  );
}
