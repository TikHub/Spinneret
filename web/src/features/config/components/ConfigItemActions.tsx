import { LoaderCircleIcon, SaveIcon, SendIcon, Trash2Icon, Undo2Icon, UndoDotIcon } from 'lucide-react';
import { type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';

import { useAuth } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';
import { PermissionButton } from '@/app/auth/PermissionGate';
import { Button } from '@/components/ui/button';
import { SimpleTooltip } from '@/components/ui/tooltip';
import { type ConfigItemInfo } from '@/gen/spinneret/v1/config_admin_pb';

export interface ConfigItemActionsProps {
  item: ConfigItemInfo;
  readOnly: boolean;
  dirty: boolean;
  /** Client-side validation errors block saving and publishing. */
  blocking: boolean;
  saving: boolean;
  onSave: () => void;
  onDiscard: () => void;
  onPublish: () => void;
  onRollback: () => void;
  onDelete: () => void;
}

/** Wraps a disabled button with a tooltip explaining why. */
function Reason({ reason, children }: { reason: string | undefined; children: ReactNode }) {
  if (!reason) return <>{children}</>;
  return (
    <SimpleTooltip content={reason}>
      <span tabIndex={0} className="inline-flex">
        {children}
      </span>
    </SimpleTooltip>
  );
}

/** Save draft, discard, publish, rollback and delete buttons with permission and state checks. */
export function ConfigItemActions({
  item,
  readOnly,
  dirty,
  blocking,
  saving,
  onSave,
  onDiscard,
  onPublish,
  onRollback,
  onDelete,
}: ConfigItemActionsProps) {
  const { t } = useTranslation('config');
  const { can } = useAuth();
  const canWrite = can(PERMISSIONS.configWrite);
  const canPublish = can(PERMISSIONS.configPublish);

  const readOnlyReason = readOnly ? t('actions.readOnly') : undefined;
  const saveReason =
    readOnlyReason ?? (blocking ? t('actions.fixErrors') : !dirty ? t('actions.noChanges') : undefined);
  const publishReason =
    readOnlyReason ??
    (blocking
      ? t('actions.fixErrors')
      : !dirty && !item.hasDraft
        ? t('actions.noDraft')
        : dirty && !canWrite
          ? t('common:permission.missing', { permission: PERMISSIONS.configWrite })
          : undefined);
  const rollbackReason =
    readOnlyReason ?? (item.currentVersion < 2 ? t('actions.noOlderVersion') : undefined);

  return (
    <>
      {dirty && (
        <Button variant="ghost" size="sm" onClick={onDiscard} disabled={saving}>
          <UndoDotIcon />
          {t('actions.discard')}
        </Button>
      )}
      <Reason reason={canWrite ? saveReason : undefined}>
        <PermissionButton
          permission={PERMISSIONS.configWrite}
          variant="outline"
          size="sm"
          disabled={saving || saveReason !== undefined}
          onClick={onSave}
        >
          {saving ? <LoaderCircleIcon className="animate-spin" /> : <SaveIcon />}
          {t('actions.saveDraft')}
        </PermissionButton>
      </Reason>
      <Reason reason={canPublish ? publishReason : undefined}>
        <PermissionButton
          permission={PERMISSIONS.configPublish}
          size="sm"
          disabled={saving || publishReason !== undefined}
          onClick={onPublish}
        >
          <SendIcon />
          {t('actions.publish')}
        </PermissionButton>
      </Reason>
      <Reason reason={canPublish ? rollbackReason : undefined}>
        <PermissionButton
          permission={PERMISSIONS.configPublish}
          variant="outline"
          size="sm"
          disabled={rollbackReason !== undefined}
          onClick={onRollback}
        >
          <Undo2Icon />
          {t('actions.rollback')}
        </PermissionButton>
      </Reason>
      <Reason reason={canPublish ? readOnlyReason : undefined}>
        <PermissionButton
          permission={PERMISSIONS.configPublish}
          variant="outline"
          size="sm"
          className="text-destructive hover:text-destructive"
          disabled={readOnlyReason !== undefined}
          onClick={onDelete}
        >
          <Trash2Icon />
          {t('common:actions.delete')}
        </PermissionButton>
      </Reason>
    </>
  );
}
