import { forwardRef, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';

import { ErrorState } from '@/components/ErrorState';
import { Button, type ButtonProps } from '@/components/ui/button';
import { SimpleTooltip } from '@/components/ui/tooltip';

import { useAuth } from './AuthContext';

export interface PermissionGateProps {
  /** Required permission, or several (see `mode`). */
  permission: string | readonly string[];
  /** "all" (default) requires every permission, "any" at least one. */
  mode?: 'all' | 'any';
  /** Site ID or name for site-scoped checks. */
  site?: string;
  /** Check tenant-level resources (users, role bindings) with canInTenant; `site` is ignored. */
  tenantLevel?: boolean;
  /** Rendered when not allowed (default: nothing). */
  fallback?: ReactNode;
  children: ReactNode | ((allowed: boolean) => ReactNode);
}

function useAllowed(
  permission: string | readonly string[],
  mode: 'all' | 'any',
  site?: string,
  tenantLevel = false,
): boolean {
  const { can, canInTenant } = useAuth();
  const list = typeof permission === 'string' ? [permission] : permission;
  const check = (p: string) => (tenantLevel ? canInTenant(p) : can(p, site));
  return mode === 'any' ? list.some(check) : list.every(check);
}

/**
 * Renders children only when the permission is granted in the active namespace.
 * With a render function the children decide (e.g. disable instead of hide).
 */
export function PermissionGate({
  permission,
  mode = 'all',
  site,
  tenantLevel,
  fallback = null,
  children,
}: PermissionGateProps) {
  const allowed = useAllowed(permission, mode, site, tenantLevel);
  if (typeof children === 'function') return <>{children(allowed)}</>;
  return <>{allowed ? children : fallback}</>;
}

export interface RequirePermissionProps extends Omit<PermissionGateProps, 'fallback' | 'children'> {
  children: ReactNode;
}

/** Page-level guard: shows the permission-denied state instead of the page. */
export function RequirePermission({
  permission,
  mode = 'all',
  site,
  tenantLevel,
  children,
}: RequirePermissionProps) {
  const { t } = useTranslation();
  const allowed = useAllowed(permission, mode, site, tenantLevel);
  if (allowed) return <>{children}</>;
  return (
    <ErrorState
      title={t('permission.deniedTitle')}
      description={t('permission.deniedDescription')}
      className="mt-6"
    />
  );
}

export interface PermissionButtonProps extends ButtonProps {
  permission: string;
  site?: string;
  /** Check a tenant-level permission (users, role bindings) with canInTenant. */
  tenantLevel?: boolean;
}

/**
 * Button disabled (with an explanatory tooltip) when the permission is missing.
 * The ref always reaches the <button>, so it works as an `asChild` trigger
 * (dropdown menus, popovers, dialogs).
 */
export const PermissionButton = forwardRef<HTMLButtonElement, PermissionButtonProps>(
  ({ permission, site, tenantLevel, disabled, ...props }, ref) => {
    const { t } = useTranslation();
    const allowed = useAllowed(permission, 'all', site, tenantLevel);
    if (allowed) return <Button ref={ref} disabled={disabled} {...props} />;
    return (
      <SimpleTooltip content={t('permission.missing', { permission })}>
        <span tabIndex={0} className="inline-flex">
          <Button ref={ref} disabled {...props} />
        </span>
      </SimpleTooltip>
    );
  },
);
PermissionButton.displayName = 'PermissionButton';
