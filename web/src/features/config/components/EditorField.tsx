import { type ReactNode } from 'react';

import { cn } from '@/lib/utils';

export interface EditorFieldProps {
  label: ReactNode;
  /** Help text under the editor. */
  description?: ReactNode;
  error?: ReactNode;
  className?: string;
  children: ReactNode;
}

/**
 * Label, error and help text around a code editor (Monaco editors take an
 * aria-label instead of an id, so FormField cannot wire them).
 */
export function EditorField({ label, description, error, className, children }: EditorFieldProps) {
  return (
    <div className={cn('grid content-start gap-1.5', className)}>
      <span className="text-sm leading-none font-medium">{label}</span>
      {children}
      {error ? (
        <p role="alert" className="text-xs text-destructive">
          {error}
        </p>
      ) : (
        description && <p className="text-xs text-muted-foreground">{description}</p>
      )}
    </div>
  );
}
