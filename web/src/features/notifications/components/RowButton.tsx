import { type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';

import { PERMISSIONS } from '@/app/auth/permissions';
import { Button } from '@/components/ui/button';
import { SimpleTooltip } from '@/components/ui/tooltip';

/** Action button disabled with a tooltip when the channel's scope lacks notify:write. */
export function RowButton({
  allowed,
  label,
  onClick,
  disabled,
  destructive,
  children,
}: {
  allowed: boolean;
  label: string;
  onClick: () => void;
  disabled?: boolean;
  destructive?: boolean;
  children: ReactNode;
}) {
  const { t } = useTranslation('notifications');
  const button = (
    <Button
      variant="ghost"
      size="icon-sm"
      aria-label={label}
      disabled={!allowed || disabled}
      className={destructive ? 'text-destructive hover:text-destructive' : undefined}
      onClick={onClick}
    >
      {children}
    </Button>
  );
  return (
    <SimpleTooltip
      content={allowed ? label : t('common:permission.missing', { permission: PERMISSIONS.notifyWrite })}
    >
      <span tabIndex={allowed ? undefined : 0} className="inline-flex">
        {button}
      </span>
    </SimpleTooltip>
  );
}
