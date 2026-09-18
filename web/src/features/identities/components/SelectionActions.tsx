import { useTranslation } from 'react-i18next';

import { PERMISSIONS } from '@/app/auth/permissions';
import { PermissionButton } from '@/app/auth/PermissionGate';
import { type Identity } from '@/gen/spinneret/v1/identity_admin_pb';

import { IDENTITY_OPERATIONS, MAX_OPERATE_IDS, type IdentityOperation } from '../operations';
import { OperationsMenu } from './OperationsMenu';

const QUICK_OPERATIONS: readonly IdentityOperation[] = ['cooldown', 'ban', 'unban'];

export interface SelectionActionsProps {
  rows: readonly Identity[];
  onOperate: (operation: IdentityOperation, rows: readonly Identity[]) => void;
}

/** Bulk operations for the selected identities (OperateIdentities). */
export function SelectionActions({ rows, onOperate }: SelectionActionsProps) {
  const { t } = useTranslation('identities');
  const tooMany = rows.length > MAX_OPERATE_IDS;
  const sites = new Set(rows.map((r) => r.site));
  const site = sites.size === 1 ? [...sites][0] : undefined;

  return (
    <>
      {QUICK_OPERATIONS.map((op) => (
        <PermissionButton
          key={op}
          permission={PERMISSIONS.identityOperate}
          site={site}
          variant="outline"
          size="sm"
          className="h-7"
          disabled={tooMany}
          onClick={() => onOperate(op, rows)}
        >
          {t(`operations.${op}`)}
        </PermissionButton>
      ))}
      <OperationsMenu
        operations={IDENTITY_OPERATIONS.filter((op) => !QUICK_OPERATIONS.includes(op))}
        onSelect={(op) => onOperate(op, rows)}
        label={t('common:actions.more')}
        site={site}
        size="sm"
        disabled={tooMany}
      />
      {tooMany && (
        <span className="text-xs text-destructive">{t('bulk.tooMany', { max: MAX_OPERATE_IDS })}</span>
      )}
    </>
  );
}
