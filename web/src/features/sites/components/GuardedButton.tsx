import { forwardRef } from 'react';
import { useTranslation } from 'react-i18next';

import { Button, type ButtonProps } from '@/components/ui/button';
import { SimpleTooltip } from '@/components/ui/tooltip';

export interface GuardedButtonProps extends ButtonProps {
  /** Result of the permission check. */
  allowed: boolean;
  /** Permission named in the tooltip when not allowed. */
  permission: string;
}

/**
 * Button disabled with an explanatory tooltip when a permission check failed.
 * Used where PermissionButton cannot express the check (namespace-wide grants).
 */
export const GuardedButton = forwardRef<HTMLButtonElement, GuardedButtonProps>(
  ({ allowed, permission, disabled, ...props }, ref) => {
    const { t } = useTranslation();
    if (allowed) return <Button ref={ref} disabled={disabled} {...props} />;
    return (
      <SimpleTooltip content={t('common:permission.missing', { permission })}>
        <span tabIndex={0} className="inline-flex">
          <Button ref={ref} disabled {...props} />
        </span>
      </SimpleTooltip>
    );
  },
);
GuardedButton.displayName = 'GuardedButton';
