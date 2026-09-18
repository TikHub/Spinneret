import { Link } from '@tanstack/react-router';
import { EllipsisIcon, FingerprintIcon, PencilIcon } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { useAuth } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';
import { Button } from '@/components/ui/button';
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import { type Account } from '@/gen/spinneret/v1/identity_admin_pb';

import { accountOperationTraits, availableAccountOperations, type AccountOperation } from '../accounts';

export interface AccountActionsMenuProps {
  account: Account;
  onOperate: (operation: AccountOperation, account: Account) => void;
  onEdit: (account: Account) => void;
}

/** Row menu: edit, operations (disabled without permission) and a link to the account's identities. */
export function AccountActionsMenu({ account, onOperate, onEdit }: AccountActionsMenuProps) {
  const { t } = useTranslation('accounts');
  const { can } = useAuth();
  const canWrite = can(PERMISSIONS.identityWrite, account.site);
  const canOperate = can(PERMISSIONS.identityOperate, account.site);
  const missingOperate = t('common:permission.missing', { permission: PERMISSIONS.identityOperate });

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="icon-sm" className="size-7" aria-label={t('columns.actions')}>
          <EllipsisIcon />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="min-w-48">
        <DropdownMenuItem
          disabled={!canWrite}
          onSelect={() => onEdit(account)}
          title={
            canWrite ? undefined : t('common:permission.missing', { permission: PERMISSIONS.identityWrite })
          }
        >
          <PencilIcon />
          {t('common:actions.edit')}
        </DropdownMenuItem>
        <DropdownMenuItem asChild>
          <Link to="/identities" search={{ site: account.site, account_ref: account.externalRef }}>
            <FingerprintIcon />
            {t('actions.viewIdentities')}
          </Link>
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        <DropdownMenuLabel>{canOperate ? t('actions.operations') : missingOperate}</DropdownMenuLabel>
        {availableAccountOperations(account.state).map((operation) => (
          <DropdownMenuItem
            key={operation}
            disabled={!canOperate}
            variant={accountOperationTraits(operation).destructive ? 'destructive' : 'default'}
            onSelect={() => onOperate(operation, account)}
          >
            {t(`operations.${operation}`)}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
