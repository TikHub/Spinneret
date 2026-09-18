import { CheckIcon, ChevronsUpDownIcon, LayersIcon } from 'lucide-react';
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

export interface NamespaceSwitcherProps {
  className?: string;
}

/**
 * Topbar dropdown selecting the active namespace of the active tenant (persisted
 * as spinneret.namespace). Asks before the switch discards unsaved editor changes.
 */
export function NamespaceSwitcher({ className }: NamespaceSwitcherProps) {
  const { t } = useTranslation();
  const { namespaces, namespaceName, setNamespace } = useAuth();
  const { runAfterDiscardConfirmed } = useUnsavedChangesRegistry();
  const select = (name: string) => {
    if (name === namespaceName) return;
    runAfterDiscardConfirmed(() => setNamespace(name));
  };
  const current = namespaces.find((n) => n.namespace?.name === namespaceName)?.namespace;
  const label = current ? current.displayName || current.name : t('shell.selectNamespace');

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="sm"
          className={cn('max-w-48 justify-between gap-1.5', className)}
          aria-label={`${t('shell.namespace')}: ${label}`}
          data-testid="namespace-switcher"
          disabled={namespaces.length === 0}
        >
          <LayersIcon className="text-muted-foreground" />
          <span className="truncate">{label}</span>
          <ChevronsUpDownIcon className="text-muted-foreground" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="w-64">
        <DropdownMenuLabel>{t('shell.namespace')}</DropdownMenuLabel>
        <DropdownMenuSeparator />
        {namespaces.length === 0 && <DropdownMenuItem disabled>{t('shell.noNamespaces')}</DropdownMenuItem>}
        {namespaces.map((access) => {
          const ns = access.namespace;
          if (!ns) return null;
          return (
            <DropdownMenuItem key={ns.id} onSelect={() => select(ns.name)}>
              <div className="flex min-w-0 flex-1 flex-col">
                <span className="truncate">{ns.displayName || ns.name}</span>
                <span className="truncate font-mono text-xs text-muted-foreground">{ns.name}</span>
              </div>
              {ns.name === namespaceName && <CheckIcon className="text-foreground" />}
            </DropdownMenuItem>
          );
        })}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
