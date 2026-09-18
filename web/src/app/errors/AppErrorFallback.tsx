import { RefreshCwIcon, TriangleAlertIcon } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { Button } from '@/components/ui/button';

export interface AppErrorFallbackProps {
  error: unknown;
  /** Try rendering again. */
  onReset?: () => void;
}

/** Fallback UI for render errors (route error component and error boundaries). */
export function AppErrorFallback({ error, onReset }: AppErrorFallbackProps) {
  const { t } = useTranslation();
  const message = error instanceof Error ? error.message : String(error);
  return (
    <div role="alert" className="mx-auto flex max-w-xl flex-col items-center gap-3 px-6 py-16 text-center">
      <div className="flex size-12 items-center justify-center rounded-full bg-destructive/10 text-destructive">
        <TriangleAlertIcon className="size-6" aria-hidden />
      </div>
      <h1 className="text-lg font-semibold">{t('errorBoundary.title')}</h1>
      <p className="text-sm text-muted-foreground">{t('errorBoundary.description')}</p>
      {message && (
        <pre className="max-h-40 w-full overflow-auto rounded-md bg-muted p-3 text-left text-xs whitespace-pre-wrap">
          {message}
        </pre>
      )}
      <div className="flex gap-2">
        {onReset && (
          <Button variant="outline" onClick={onReset}>
            <RefreshCwIcon />
            {t('actions.retry')}
          </Button>
        )}
        <Button onClick={() => window.location.reload()}>{t('actions.reload')}</Button>
      </div>
    </div>
  );
}
