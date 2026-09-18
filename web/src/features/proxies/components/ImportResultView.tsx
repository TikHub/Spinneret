import { CircleCheckIcon, FlaskConicalIcon } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { type ImportProxiesResponse } from '@/gen/spinneret/v1/proxy_admin_pb';
import { formatNumber } from '@/lib/format';
import { cn } from '@/lib/utils';

export interface ImportResultViewProps {
  result: ImportProxiesResponse;
  dryRun: boolean;
}

function Count({ label, value, tone }: { label: string; value: number; tone?: 'good' | 'bad' }) {
  const { i18n } = useTranslation();
  return (
    <div className="rounded-md border px-3 py-2">
      <div className="text-xs text-muted-foreground">{label}</div>
      <div
        className={cn(
          'tabular text-lg font-semibold',
          tone === 'good' && value > 0 && 'text-emerald-600 dark:text-emerald-400',
          tone === 'bad' && value > 0 && 'text-rose-600 dark:text-rose-400',
        )}
      >
        {formatNumber(value, undefined, i18n.language)}
      </div>
    </div>
  );
}

/** Counts and rejected rows of an import or dry run. */
export function ImportResultView({ result, dryRun }: ImportResultViewProps) {
  const { t } = useTranslation('proxies');
  const Icon = dryRun ? FlaskConicalIcon : CircleCheckIcon;
  return (
    <section className="grid gap-3" aria-live="polite">
      <h3 className="flex items-center gap-2 text-sm font-semibold">
        <Icon className="size-4 text-muted-foreground" aria-hidden />
        {dryRun ? t('import.dryRunResult') : t('import.importResult')}
      </h3>
      <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
        <Count
          label={dryRun ? t('import.wouldCreate') : t('import.created')}
          value={result.created}
          tone="good"
        />
        <Count label={dryRun ? t('import.wouldUpdate') : t('import.updated')} value={result.updated} />
        <Count label={t('import.unchanged')} value={result.unchanged} />
        <Count label={t('import.failed')} value={result.failed.length} tone="bad" />
      </div>
      {result.failed.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t('import.noFailures')}</p>
      ) : (
        <div className="max-h-56 overflow-auto rounded-md border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="w-20 text-right">{t('import.line')}</TableHead>
                <TableHead>{t('import.message')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {result.failed.map((failure, index) => (
                <TableRow key={`${failure.line}-${index}`}>
                  <TableCell className="tabular text-right">{failure.line}</TableCell>
                  <TableCell className="text-xs break-words whitespace-normal">{failure.message}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}
    </section>
  );
}
