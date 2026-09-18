import { ChevronDownIcon } from 'lucide-react';
import { Fragment, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';

import { useAuth } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';
import { PermissionButton } from '@/app/auth/PermissionGate';
import { Button, type ButtonProps } from '@/components/ui/button';
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';

import {
  operationTraits,
  RESTORING_OPERATIONS,
  RESTRICTING_OPERATIONS,
  type IdentityOperation,
} from '../operations';

export interface OperationsMenuProps {
  /** Operations to offer (in menu order groups). */
  operations: readonly IdentityOperation[];
  onSelect: (operation: IdentityOperation) => void;
  label: string;
  /** Site for the identity:operate check. */
  site?: string;
  heading?: string;
  variant?: ButtonProps['variant'];
  size?: ButtonProps['size'];
  disabled?: boolean;
  icon?: ReactNode;
}

/** Dropdown of manual identity operations; disabled with a tooltip without identity:operate. */
export function OperationsMenu({
  operations,
  onSelect,
  label,
  site,
  heading,
  variant = 'outline',
  size = 'sm',
  disabled,
  icon,
}: OperationsMenuProps) {
  const { t } = useTranslation('identities');
  const { can } = useAuth();
  if (!can(PERMISSIONS.identityOperate, site)) {
    return (
      <PermissionButton permission={PERMISSIONS.identityOperate} site={site} variant={variant} size={size}>
        {icon}
        {label}
        <ChevronDownIcon />
      </PermissionButton>
    );
  }
  const groups = [RESTRICTING_OPERATIONS, RESTORING_OPERATIONS]
    .map((group) => group.filter((op) => operations.includes(op)))
    .filter((group) => group.length > 0);

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant={variant} size={size} disabled={disabled || operations.length === 0}>
          {icon}
          {label}
          <ChevronDownIcon />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="min-w-48">
        {heading && <DropdownMenuLabel>{heading}</DropdownMenuLabel>}
        {groups.map((group, index) => (
          <Fragment key={group.join()}>
            {index > 0 && <DropdownMenuSeparator />}
            {group.map((op) => (
              <DropdownMenuItem
                key={op}
                variant={operationTraits(op).destructive ? 'destructive' : 'default'}
                onSelect={() => onSelect(op)}
              >
                {t(`operations.${op}`)}
              </DropdownMenuItem>
            ))}
          </Fragment>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
