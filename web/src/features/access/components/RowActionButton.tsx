import { type LucideIcon } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { Button } from '@/components/ui/button';
import { SimpleTooltip } from '@/components/ui/tooltip';
import { cn } from '@/lib/utils';

export interface RowActionButtonProps {
  icon: LucideIcon;
  /** Accessible label and tooltip. */
  label: string;
  onClick: () => void;
  /** When false the button is disabled and the tooltip names the missing permission. */
  allowed?: boolean;
  /** Permission shown in the tooltip when not allowed. */
  permission?: string;
  /** Disabled for a reason other than permissions (explained by disabledReason). */
  disabled?: boolean;
  disabledReason?: string;
  destructive?: boolean;
}

/**
 * Icon button for table row actions: always labelled with a tooltip, and
 * disabled (not hidden) with an explanation when the action is not allowed.
 */
export function RowActionButton({
  icon: Icon,
  label,
  onClick,
  allowed = true,
  permission,
  disabled = false,
  disabledReason,
  destructive = false,
}: RowActionButtonProps) {
  const { t } = useTranslation();
  const blocked = !allowed || disabled;
  const reason = !allowed
    ? permission
      ? t('permission.missing', { permission })
      : t('permission.deniedTitle')
    : disabledReason;
  const button = (
    <Button
      type="button"
      variant="ghost"
      size="icon-sm"
      className={cn('size-7', destructive && 'text-destructive hover:text-destructive')}
      aria-label={label}
      disabled={blocked}
      onClick={(event) => {
        event.stopPropagation();
        onClick();
      }}
    >
      <Icon className="size-4" />
    </Button>
  );
  if (!blocked) return <SimpleTooltip content={label}>{button}</SimpleTooltip>;
  return (
    <SimpleTooltip content={reason ? `${label} · ${reason}` : label}>
      <span
        tabIndex={0}
        className="inline-flex rounded-md outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        {button}
      </span>
    </SimpleTooltip>
  );
}
