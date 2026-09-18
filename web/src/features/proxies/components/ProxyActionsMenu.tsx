import { EllipsisIcon } from 'lucide-react';
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
import { type Proxy } from '@/gen/spinneret/v1/proxy_admin_pb';

import { OPERATION_SPECS, operationsForState, type ProxyOperation } from '../proxyOperations';

/** Single-proxy actions offered by the row and detail menus. */
export type ProxyAction = 'details' | 'edit' | 'check' | 'delete' | ProxyOperation;

export interface ProxyActionsMenuProps {
  proxy: Proxy;
  onAction: (action: ProxyAction, proxy: Proxy) => void;
  /** Hide the "details" entry (inside the detail sheet). */
  hideDetails?: boolean;
  triggerVariant?: 'ghost' | 'outline';
}

/** Dropdown of single-proxy actions; entries without permission are disabled and explained. */
export function ProxyActionsMenu({
  proxy,
  onAction,
  hideDetails = false,
  triggerVariant = 'ghost',
}: ProxyActionsMenuProps) {
  const { t } = useTranslation('proxies');
  const { can } = useAuth();
  const canWrite = can(PERMISSIONS.proxyWrite);
  const canOperate = can(PERMISSIONS.proxyOperate);
  const missing = [
    !canOperate ? PERMISSIONS.proxyOperate : undefined,
    !canWrite ? PERMISSIONS.proxyWrite : undefined,
  ].filter(Boolean);
  const name = proxy.displayUrl || proxy.id;

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant={triggerVariant}
          size="icon-sm"
          className={triggerVariant === 'ghost' ? 'size-7' : undefined}
          aria-label={t('rowActions.open', { name })}
        >
          <EllipsisIcon />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="min-w-44">
        {missing.length > 0 && (
          <DropdownMenuLabel className="max-w-60 font-normal">
            {t('rowActions.readOnly', { permissions: missing.join(', ') })}
          </DropdownMenuLabel>
        )}
        {!hideDetails && (
          <DropdownMenuItem onSelect={() => onAction('details', proxy)}>
            {t('rowActions.details')}
          </DropdownMenuItem>
        )}
        <DropdownMenuItem disabled={!canOperate} onSelect={() => onAction('check', proxy)}>
          {t('check.now')}
        </DropdownMenuItem>
        <DropdownMenuItem disabled={!canWrite} onSelect={() => onAction('edit', proxy)}>
          {t('common:actions.edit')}
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        {operationsForState(proxy.state).map((op) => (
          <DropdownMenuItem
            key={op}
            disabled={!canOperate}
            variant={OPERATION_SPECS[op].destructive ? 'destructive' : 'default'}
            onSelect={() => onAction(op, proxy)}
          >
            {t(`operations.${op}`)}
          </DropdownMenuItem>
        ))}
        <DropdownMenuSeparator />
        <DropdownMenuItem
          variant="destructive"
          disabled={!canWrite}
          onSelect={() => onAction('delete', proxy)}
        >
          {t('common:actions.delete')}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
