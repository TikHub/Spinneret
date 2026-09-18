import { useTranslation } from 'react-i18next';

import { ConfirmDialog } from '@/components/ConfirmDialog';
import { type DeleteProxiesResponse } from '@/gen/spinneret/v1/proxy_admin_pb';

import { MAX_BULK_IDS, uniqueIds } from '../proxyOperations';
import { useDeleteProxies } from '../useProxies';

/** Typed confirmation text of proxy deletion. */
export const DELETE_CONFIRM_TEXT = 'delete';

export interface DeleteProxiesDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  ids: readonly string[];
  /** Identities bound to the selected proxies (known for the loaded rows). */
  boundIdentities: number;
  subject?: string;
  onDone: (ids: readonly string[], response: DeleteProxiesResponse) => void;
}

/** Permanent deletion of proxies with typed confirmation. */
export function DeleteProxiesDialog({
  open,
  onOpenChange,
  ids,
  boundIdentities,
  subject,
  onDone,
}: DeleteProxiesDialogProps) {
  const { t } = useTranslation('proxies');
  const mutation = useDeleteProxies();
  const unique = uniqueIds(ids);

  const confirm = async () => {
    const response = await mutation.mutateAsync(unique);
    onDone(unique, response);
  };

  return (
    <ConfirmDialog
      open={open}
      onOpenChange={onOpenChange}
      destructive
      title={subject ? t('delete.titleOne', { name: subject }) : t('delete.title', { count: unique.length })}
      description={t('delete.description')}
      affectedCount={subject ? undefined : unique.length}
      confirmLabel={t('common:actions.delete')}
      confirmText={DELETE_CONFIRM_TEXT}
      confirmDisabled={unique.length === 0 || unique.length > MAX_BULK_IDS}
      onConfirm={confirm}
    >
      {boundIdentities > 0 && (
        <p className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm text-destructive">
          {t('delete.boundWarning', { count: boundIdentities })}
        </p>
      )}
    </ConfirmDialog>
  );
}
