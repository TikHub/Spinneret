import { Link } from '@tanstack/react-router';
import { DatabaseZapIcon, RefreshCwIcon, ShieldAlertIcon } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { Button } from '@/components/ui/button';
import { describeError } from '@/lib/errors';

export const CLICKHOUSE_ENV = 'SPINNERET_CLICKHOUSE_URL';

export interface ClickHouseDisabledProps {
  error: unknown;
  onRetry: () => void;
}

/** Explains that the request explorer needs ClickHouse and how to enable it. */
export function ClickHouseDisabled({ error, onRetry }: ClickHouseDisabledProps) {
  const { t } = useTranslation('requests');
  const detail = describeError(error, t).detail;
  return (
    <div
      role="status"
      className="flex flex-col items-center justify-center gap-3 rounded-lg border border-dashed px-6 py-14 text-center"
    >
      <div className="flex size-10 items-center justify-center rounded-full bg-muted text-muted-foreground">
        <DatabaseZapIcon className="size-5" aria-hidden />
      </div>
      <p className="text-sm font-medium">{t('clickhouse.title')}</p>
      <p className="max-w-xl text-sm text-muted-foreground">{t('clickhouse.description')}</p>
      <div className="w-full max-w-xl rounded-md border bg-muted/50 p-3 text-left">
        <p className="mb-1 text-xs text-muted-foreground">{t('clickhouse.howTo')}</p>
        <code className="block font-mono text-xs break-all">
          {CLICKHOUSE_ENV}=clickhouse://user:password@clickhouse:9000/spinneret
        </code>
      </div>
      <p className="max-w-xl text-xs text-muted-foreground">{t('clickhouse.riskEventsHint')}</p>
      {detail && (
        <code className="rounded bg-muted px-1.5 py-0.5 text-xs text-muted-foreground">{detail}</code>
      )}
      <div className="flex flex-wrap items-center justify-center gap-2">
        <Button variant="outline" size="sm" onClick={onRetry}>
          <RefreshCwIcon />
          {t('common:actions.retry')}
        </Button>
        <Button variant="secondary" size="sm" asChild>
          <Link to="/risk-events">
            <ShieldAlertIcon />
            {t('clickhouse.openRiskEvents')}
          </Link>
        </Button>
      </div>
    </div>
  );
}
