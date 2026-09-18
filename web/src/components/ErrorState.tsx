import { Code } from '@connectrpc/connect';
import { LockIcon, RefreshCwIcon, TriangleAlertIcon } from 'lucide-react';
import { type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';

import { Button } from '@/components/ui/button';
import { describeError, toApiError } from '@/lib/errors';
import { cn } from '@/lib/utils';

export interface ErrorStateProps {
  /** The thrown error (ConnectError or any value). */
  error?: unknown;
  /** Overrides the derived title. */
  title?: ReactNode;
  /** Overrides the derived description. */
  description?: ReactNode;
  onRetry?: () => void;
  className?: string;
  compact?: boolean;
}

/** Error panel with translated reason, raw server message and a retry button. */
export function ErrorState({ error, title, description, onRetry, className, compact }: ErrorStateProps) {
  const { t } = useTranslation();
  const denied = error !== undefined && toApiError(error).code === Code.PermissionDenied;
  const described = error === undefined ? undefined : describeError(error, t);
  const Icon = denied ? LockIcon : TriangleAlertIcon;

  return (
    <div
      role="alert"
      className={cn(
        'flex flex-col items-center justify-center gap-2 text-center',
        compact ? 'px-4 py-8' : 'rounded-lg border border-dashed px-6 py-16',
        className,
      )}
    >
      <div
        className={cn(
          'flex size-10 items-center justify-center rounded-full',
          denied ? 'bg-muted text-muted-foreground' : 'bg-destructive/10 text-destructive',
        )}
      >
        <Icon className="size-5" aria-hidden />
      </div>
      <p className="text-sm font-medium">
        {title ?? (denied ? t('permission.deniedTitle') : (described?.title ?? t('errorBoundary.title')))}
      </p>
      {(description ?? described?.detail) && (
        <p className="max-w-lg text-sm break-words text-muted-foreground">
          {description ?? described?.detail}
        </p>
      )}
      {described?.reason && (
        <code className="rounded bg-muted px-1.5 py-0.5 text-xs text-muted-foreground">
          {described.reason}
        </code>
      )}
      {onRetry && !denied && (
        <Button variant="outline" size="sm" className="mt-2" onClick={onRetry}>
          <RefreshCwIcon />
          {t('actions.retry')}
        </Button>
      )}
    </div>
  );
}
