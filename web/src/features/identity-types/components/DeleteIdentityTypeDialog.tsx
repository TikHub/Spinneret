import { TriangleAlertIcon } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import { ConfirmDialog } from '@/components/ConfirmDialog';
import { useInvalidateIdentityData } from '@/features/identities/notify';
import { type IdentityType } from '@/gen/spinneret/v1/identity_admin_pb';
import { identityClient } from '@/lib/clients';
import { describeError } from '@/lib/errors';
import { formatNumber } from '@/lib/format';

export interface DeleteIdentityTypeDialogProps {
  identityType: IdentityType;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

/** Typed confirmation for DeleteIdentityType; the server refuses while identities exist and the reason is shown. */
export function DeleteIdentityTypeDialog({
  identityType,
  open,
  onOpenChange,
}: DeleteIdentityTypeDialogProps) {
  const { t, i18n } = useTranslation('identity-types');
  const invalidate = useInvalidateIdentityData();
  const [failure, setFailure] = useState<{ title: string; detail?: string }>();

  return (
    <ConfirmDialog
      open={open}
      onOpenChange={onOpenChange}
      title={t('delete.title', { name: identityType.name })}
      description={t('delete.description', { site: identityType.site })}
      destructive
      confirmText={identityType.name}
      confirmLabel={t('common:actions.delete')}
      onConfirm={async () => {
        setFailure(undefined);
        try {
          await identityClient.deleteIdentityType({ id: identityType.id });
        } catch (err) {
          const described = describeError(err, t);
          setFailure({ title: described.title, detail: described.detail });
          throw err;
        }
        toast.success(t('delete.deleted', { name: identityType.name }));
        invalidate();
      }}
    >
      {identityType.identityCount > 0 && (
        <p className="flex items-start gap-2 rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-sm text-amber-700 dark:text-amber-400">
          <TriangleAlertIcon className="mt-0.5 size-4 shrink-0" aria-hidden />
          {t('delete.hasIdentities', {
            count: identityType.identityCount,
            formatted: formatNumber(identityType.identityCount, undefined, i18n.language),
          })}
        </p>
      )}
      {failure && (
        <p
          role="alert"
          className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm text-destructive"
        >
          <span className="font-medium">{failure.title}</span>
          {failure.detail && failure.detail !== failure.title && (
            <span className="block">{failure.detail}</span>
          )}
        </p>
      )}
    </ConfirmDialog>
  );
}
