import { useState } from 'react';
import { useTranslation } from 'react-i18next';

import { ConfirmDialog } from '@/components/ConfirmDialog';
import { DurationInput } from '@/components/DurationInput';
import { FormField, FormStack } from '@/components/ui/form';
import { Textarea } from '@/components/ui/textarea';
import { MAX_REASON_LENGTH } from '@/features/identities/operations';
import { type Account, type OperateAccountResponse } from '@/gen/spinneret/v1/identity_admin_pb';
import { identityClient } from '@/lib/clients';

import {
  accountOperationTraits,
  buildOperateAccountRequest,
  defaultAccountOperationForm,
  isPermanentAccountBan,
  validateAccountOperation,
  type AccountOperation,
  type AccountOperationForm,
} from '../accounts';

export interface AccountOperationDialogProps {
  account: Account;
  operation: AccountOperation;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onApplied: (response: OperateAccountResponse) => void;
}

/** Confirms an account operation (OperateAccount); bans and disables also affect the account's identities. */
export function AccountOperationDialog({
  account,
  operation,
  open,
  onOpenChange,
  onApplied,
}: AccountOperationDialogProps) {
  const { t } = useTranslation('accounts');
  const [form, setForm] = useState<AccountOperationForm>(() => defaultAccountOperationForm(operation));
  const traits = accountOperationTraits(operation);
  const errors = validateAccountOperation(form);
  const label = t(`operations.${operation}`);

  return (
    <ConfirmDialog
      open={open}
      onOpenChange={onOpenChange}
      title={t('operation.title', { operation: label, ref: account.externalRef })}
      description={t(`operationHints.${operation}`, { count: account.identityCount })}
      destructive={traits.destructive}
      affectedCount={account.identityCount}
      confirmText={isPermanentAccountBan(form) ? 'ban' : undefined}
      confirmLabel={label}
      confirmDisabled={errors.duration !== undefined || errors.reason !== undefined}
      onConfirm={async () => {
        const response = await identityClient.operateAccount(buildOperateAccountRequest(account.id, form));
        onApplied(response);
      }}
    >
      <FormStack className="gap-3">
        {traits.needsDuration && (
          <FormField
            label={t('operation.duration')}
            required
            description={t(operation === 'ban' ? 'operation.banHint' : 'operation.cooldownHint')}
            error={
              errors.duration === 'required' && form.duration.trim() !== ''
                ? t('identities:operation.errors.durationPositive')
                : undefined
            }
          >
            <DurationInput
              value={form.duration}
              onChange={(duration) => setForm({ ...form, duration })}
              allowPermanent={traits.allowPermanent}
            />
          </FormField>
        )}
        <FormField
          label={t('operation.reason')}
          error={
            errors.reason
              ? t('identities:operation.errors.reasonTooLong', { max: MAX_REASON_LENGTH })
              : undefined
          }
        >
          <Textarea
            rows={2}
            value={form.reason}
            maxLength={MAX_REASON_LENGTH + 1}
            placeholder={t('identities:operation.reasonPlaceholder')}
            onChange={(e) => setForm({ ...form, reason: e.target.value })}
          />
        </FormField>
      </FormStack>
    </ConfirmDialog>
  );
}
