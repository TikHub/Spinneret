import { InboxIcon, type LucideIcon } from 'lucide-react';
import { type ReactNode } from 'react';

import { cn } from '@/lib/utils';

export interface EmptyStateProps {
  title: ReactNode;
  description?: ReactNode;
  icon?: LucideIcon;
  /** Call to action (e.g. a create button). */
  action?: ReactNode;
  className?: string;
  /** Smaller padding for use inside cards and tables. */
  compact?: boolean;
}

/** Centered placeholder for empty lists and pages. */
export function EmptyState({
  title,
  description,
  icon: Icon = InboxIcon,
  action,
  className,
  compact,
}: EmptyStateProps) {
  return (
    <div
      className={cn(
        'flex flex-col items-center justify-center gap-2 text-center',
        compact ? 'px-4 py-8' : 'rounded-lg border border-dashed px-6 py-16',
        className,
      )}
    >
      <div className="flex size-10 items-center justify-center rounded-full bg-muted text-muted-foreground">
        <Icon className="size-5" aria-hidden />
      </div>
      <p className="text-sm font-medium">{title}</p>
      {description && <p className="max-w-md text-sm text-muted-foreground">{description}</p>}
      {action && <div className="mt-2">{action}</div>}
    </div>
  );
}
