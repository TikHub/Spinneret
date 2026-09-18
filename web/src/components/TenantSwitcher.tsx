import { Building2Icon, CheckIcon, ChevronsUpDownIcon } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { useAuth } from '@/app/auth/AuthContext';
import { useUnsavedChangesRegistry } from '@/app/unsaved/useUnsavedChanges';
import { Button } from '@/components/ui/button';
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import { cn } from '@/lib/utils';

export interface TenantSwitcherProps {
  className?: string;
}

/**
 * Topbar dropdown selecting the active tenant (persisted as spinneret.tenant).
 * Asks before the switch discards unsaved editor changes.
 */
export function TenantSwitcher({ className }: TenantSwitcherProps) {
  const { t } = useTranslation();
  const { tenants, tenant, setTenant } = useAuth();
  const { runAfterDiscardConfirmed } = useUnsavedChangesRegistry();
  const current = tenant?.tenant;
  const label = current ? current.displayName || current.name : t('shell.selectTenant');
  const select = (id: string) => {
    if (id === current?.id) return;
    runAfterDiscardConfirmed(() => setTenant(id));
  };

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="sm"
          className={cn('max-w-48 justify-between gap-1.5', className)}
          aria-label={`${t('shell.tenant')}: ${label}`}
          data-testid="tenant-switcher"
          disabled={tenants.length === 0}
        >
          <Building2Icon className="text-muted-foreground" />
          <span className="truncate">{label}</span>
          <ChevronsUpDownIcon className="text-muted-foreground" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="w-64">
        <DropdownMenuLabel>{t('shell.tenant')}</DropdownMenuLabel>
        <DropdownMenuSeparator />
        {tenants.length === 0 && <DropdownMenuItem disabled>{t('shell.noTenants')}</DropdownMenuItem>}
        {tenants.map((access) => {
          const item = access.tenant;
          if (!item) return null;
          const selected = item.id === current?.id;
          return (
            <DropdownMenuItem key={item.id} onSelect={() => select(item.id)}>
              <div className="flex min-w-0 flex-1 flex-col">
                <span className="truncate">{item.displayName || item.name}</span>
                <span className="truncate font-mono text-xs text-muted-foreground">{item.name}</span>
              </div>
              {selected && <CheckIcon className="text-foreground" />}
            </DropdownMenuItem>
          );
        })}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
