import { type ReactNode } from 'react';

import { cn } from '@/lib/utils';

export interface PageHeaderProps {
  title: ReactNode;
  description?: ReactNode;
  /** Right-aligned actions (buttons, selects). */
  actions?: ReactNode;
  /** Optional content under the title row (tabs, filter bar). */
  children?: ReactNode;
  className?: string;
}

/** Page title row with description and actions. */
export function PageHeader({ title, description, actions, children, className }: PageHeaderProps) {
  return (
    <div className={cn('flex flex-col gap-3 pb-4', className)}>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 space-y-1">
          <h1 className="truncate text-xl font-semibold tracking-tight">{title}</h1>
          {description && <p className="text-sm text-muted-foreground">{description}</p>}
        </div>
        {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
      </div>
      {children}
    </div>
  );
}
