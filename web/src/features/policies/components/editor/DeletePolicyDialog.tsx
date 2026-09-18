import { TriangleAlertIcon } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { ConfirmDialog } from '@/components/ConfirmDialog';
import { type Policy } from '@/gen/spinneret/v1/policy_admin_pb';

import { useDeletePolicy } from '../../usePolicyMutations';

export interface DeletePolicyDialogProps {
  policy: Policy;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onDeleted: () => void;
}

/** Deletes an unbound policy with typed confirmation of its name. */
export function DeletePolicyDialog({ policy, open, onOpenChange, onDeleted }: DeletePolicyDialogProps) {
  const { t } = useTranslation('policies');
  const remove = useDeletePolicy();
  const bound = policy.bindings.length;

  return (
    <ConfirmDialog
      open={open}
      onOpenChange={onOpenChange}
      destructive
      title={t('delete.title', { name: policy.name })}
      description={t('delete.description')}
      confirmLabel={t('delete.confirm')}
      confirmText={policy.name}
      confirmDisabled={bound > 0}
      onConfirm={async () => {
        await remove.mutateAsync(policy.id);
        onDeleted();
      }}
    >
      {bound > 0 && (
        <p
          role="alert"
          className="flex items-start gap-2 rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-sm"
        >
          <TriangleAlertIcon className="mt-0.5 size-4 shrink-0 text-amber-600" aria-hidden />
          {t('delete.bound', { count: bound })}
        </p>
      )}
    </ConfirmDialog>
  );
}
