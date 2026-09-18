import { CircleCheckIcon, FlaskConicalIcon } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { type ImportIdentitiesResponse } from '@/gen/spinneret/v1/identity_admin_pb';
import { formatNumber } from '@/lib/format';
import { cn } from '@/lib/utils';

import { summarizeImport } from '../importData';

export interface ImportResultViewProps {
  result: Pick<ImportIdentitiesResponse, 'created' | 'updated' | 'unchanged' | 'failed'>;
  /** The result of a validation run (nothing stored). */
  dryRun: boolean;
}

function Tile({ label, value, tone }: { label: string; value: string; tone?: 'danger' | 'success' }) {
  return (
    <div className="rounded-md border px-3 py-2">
      <div className="text-xs text-muted-foreground">{label}</div>
      <div
        className={cn(
          'tabular text-lg font-semibold',
          tone === 'success' && 'text-emerald-600 dark:text-emerald-400',
          tone === 'danger' && 'text-rose-600 dark:text-rose-400',
        )}
      >
        {value}
      </div>
    </div>
  );
}

/** Created / updated / unchanged counts and the rejected rows of an import or dry run. */
export function ImportResultView({ result, dryRun }: ImportResultViewProps) {
  const { t, i18n } = useTranslation('identities');
  const summary = summarizeImport(result);
  const fmt = (n: number) => formatNumber(n, undefined, i18n.language);

  return (
    <section className="grid gap-3" aria-label={t('import.resultTitle')}>
      <div className="flex items-center gap-2 text-sm font-medium">
        {dryRun ? (
          <FlaskConicalIcon className="size-4 text-sky-500" aria-hidden />
        ) : (
          <CircleCheckIcon className="size-4 text-emerald-500" aria-hidden />
        )}
        {dryRun ? t('import.dryRunResult') : t('import.importResult')}
        <span className="font-normal text-muted-foreground">
          {t('import.rowsProcessed', { count: summary.total, formatted: fmt(summary.total) })}
        </span>
      </div>
      <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
        <Tile
          label={dryRun ? t('import.wouldCreate') : t('import.created')}
          value={fmt(summary.created)}
          tone="success"
        />
        <Tile label={dryRun ? t('import.wouldUpdate') : t('import.updated')} value={fmt(summary.updated)} />
        <Tile label={t('import.unchanged')} value={fmt(summary.unchanged)} />
        <Tile
          label={t('import.failed')}
          value={fmt(summary.failed)}
          tone={summary.failed > 0 ? 'danger' : undefined}
        />
      </div>
      {summary.failed > 0 && (
        <div className="grid gap-1.5">
          <p className="text-sm font-medium">{t('import.failuresTitle')}</p>
          <div className="max-h-64 overflow-auto rounded-md border">
            <table className="w-full text-xs">
              <thead className="sticky top-0 bg-muted">
                <tr className="text-left text-muted-foreground">
                  <th scope="col" className="w-20 px-2 py-1.5 font-medium">
                    {t('import.line')}
                  </th>
                  <th scope="col" className="px-2 py-1.5 font-medium">
                    {t('import.message')}
                  </th>
                </tr>
              </thead>
              <tbody>
                {summary.shownFailures.map((failure, index) => (
                  <tr key={`${failure.line}-${index}`} className="border-t align-top">
                    <td className="tabular px-2 py-1.5 font-mono">{failure.line}</td>
                    <td className="px-2 py-1.5 break-words">{failure.message}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {summary.hiddenFailures > 0 && (
            <p className="text-xs text-muted-foreground">
              {t('import.moreFailures', {
                count: summary.hiddenFailures,
                formatted: fmt(summary.hiddenFailures),
              })}
            </p>
          )}
        </div>
      )}
    </section>
  );
}
