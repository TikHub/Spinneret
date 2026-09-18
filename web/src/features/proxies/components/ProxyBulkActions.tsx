import { ChevronDownIcon, Trash2Icon } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { useAuth } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';
import { PermissionButton } from '@/app/auth/PermissionGate';
import { Button } from '@/components/ui/button';
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';

import { OPERATION_SPECS, type ProxyBulkAction, type ProxyOperation } from '../proxyOperations';

const PRIMARY_OPERATIONS: readonly ProxyOperation[] = ['enable', 'disable', 'cooldown', 'ban', 'unban'];
const MORE_OPERATIONS: readonly ProxyOperation[] = [
  'quarantine',
  'unquarantine',
  'reset_stats',
  'archive',
  'restore',
];

export interface ProxyBulkActionsProps {
  onAction: (action: ProxyBulkAction) => void;
}

/** Buttons shown in the selection bar of the proxies table. */
export function ProxyBulkActions({ onAction }: ProxyBulkActionsProps) {
  const { t } = useTranslation('proxies');
  const canOperate = useAuth().can(PERMISSIONS.proxyOperate);

  return (
    <>
      {PRIMARY_OPERATIONS.map((op) => (
        <PermissionButton
          key={op}
          permission={PERMISSIONS.proxyOperate}
          variant="outline"
          size="sm"
          className="h-7"
          onClick={() => onAction(op)}
        >
          {t(`operations.${op}`)}
        </PermissionButton>
      ))}
      {canOperate ? (
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="outline" size="sm" className="h-7">
              {t('bulk.more')}
              <ChevronDownIcon />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start">
            {MORE_OPERATIONS.map((op) => (
              <DropdownMenuItem
                key={op}
                variant={OPERATION_SPECS[op].destructive ? 'destructive' : 'default'}
                onSelect={() => onAction(op)}
              >
                {t(`operations.${op}`)}
              </DropdownMenuItem>
            ))}
          </DropdownMenuContent>
        </DropdownMenu>
      ) : (
        <PermissionButton permission={PERMISSIONS.proxyOperate} variant="outline" size="sm" className="h-7">
          {t('bulk.more')}
          <ChevronDownIcon />
        </PermissionButton>
      )}
      <PermissionButton
        permission={PERMISSIONS.proxyWrite}
        variant="destructive"
        size="sm"
        className="h-7"
        onClick={() => onAction('delete')}
      >
        <Trash2Icon />
        {t('common:actions.delete')}
      </PermissionButton>
    </>
  );
}
