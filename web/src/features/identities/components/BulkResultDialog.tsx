import { Link } from '@tanstack/react-router';
import { CircleCheckIcon, CircleXIcon, ListChecksIcon } from 'lucide-react';
import { type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';

import { CopyButton } from '@/components/CopyButton';
import { Button } from '@/components/ui/button';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { type BulkResult } from '@/gen/spinneret/v1/common_pb';
import { formatNumber } from '@/lib/format';
import { cn } from '@/lib/utils';

import { failureReasonText } from '../notify';
import { summarizeBulkResult } from '../operations';

/** Failures rendered in the table; the rest are only counted. */
const FAILURES_SHOWN = 500;

function Stat({ label, value, tone }: { label: string; value: number; tone?: 'success' | 'danger' }) {
  const { i18n } = useTranslation();
  return (
    <div className="rounded-md border px-3 py-2">
      <div className="text-xs text-muted-foreground">{label}</div>
      <div
        className={cn(
          'tabular text-lg font-semibold',
          tone === 'success' && 'text-emerald-600 dark:text-emerald-400',
          tone === 'danger' && value > 0 && 'text-rose-600 dark:text-rose-400',
        )}
      >
        {formatNumber(value, undefined, i18n.language)}
      </div>
    </div>
  );
}

export interface BulkResultViewProps {
  result: BulkResult | undefined;
  /** Link failure IDs to the identity detail page. */
  linkIdentities?: boolean;
}

/** Matched / succeeded / failed counts with the failure list. */
export function BulkResultView({ result, linkIdentities = true }: BulkResultViewProps) {
  const { t } = useTranslation('identities');
  const summary = summarizeBulkResult(result);
  const failures = result?.failed ?? [];
  const shown = failures.slice(0, FAILURES_SHOWN);

  return (
    <div className="grid gap-3">
      <div className="grid grid-cols-3 gap-2">
        <Stat label={t('result.matched')} value={summary.matched} />
        <Stat label={t('result.succeeded')} value={summary.succeeded} tone="success" />
        <Stat label={t('result.failed')} value={summary.failed} tone="danger" />
      </div>
      {failures.length === 0 ? (
        <p className="flex items-center gap-2 text-sm text-muted-foreground">
          <CircleCheckIcon className="size-4 text-emerald-500" aria-hidden />
          {t('result.noFailures')}
        </p>
      ) : (
        <div className="grid gap-2">
          <div className="flex items-center justify-between gap-2">
            <p className="flex items-center gap-2 text-sm font-medium">
              <CircleXIcon className="size-4 text-rose-500" aria-hidden />
              {t('result.failures')}
            </p>
            <CopyButton
              value={failures.map((f) => f.id).join('\n')}
              label={t('result.copyFailedIds')}
              className="size-7"
            />
          </div>
          <div className="max-h-72 overflow-auto rounded-md border">
            <table className="w-full text-xs">
              <thead className="sticky top-0 bg-muted">
                <tr className="text-left text-muted-foreground">
                  <th className="px-2 py-1.5 font-medium">{t('result.id')}</th>
                  <th className="px-2 py-1.5 font-medium">{t('result.reason')}</th>
                  <th className="px-2 py-1.5 font-medium">{t('result.message')}</th>
                </tr>
              </thead>
              <tbody>
                {shown.map((failure, index) => (
                  <tr key={`${failure.id}-${index}`} className="border-t align-top">
                    <td className="px-2 py-1.5 font-mono whitespace-nowrap">
                      {linkIdentities && failure.id ? (
                        <Link to="/identities/$id" params={{ id: failure.id }} className="hover:underline">
                          {failure.id}
                        </Link>
                      ) : (
                        failure.id || '—'
                      )}
                    </td>
                    <td className="px-2 py-1.5">
                      <span title={failure.reason}>{failureReasonText(t, failure) || '—'}</span>
                    </td>
                    <td className="px-2 py-1.5 break-words text-muted-foreground">
                      {failure.message || '—'}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {failures.length > shown.length && (
            <p className="text-xs text-muted-foreground">
              {t('result.moreFailures', { count: failures.length - shown.length })}
            </p>
          )}
        </div>
      )}
    </div>
  );
}

export interface BulkResultDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: ReactNode;
  description?: ReactNode;
  result: BulkResult | undefined;
  linkIdentities?: boolean;
  /** Extra content under the result (e.g. affected IDs). */
  children?: ReactNode;
}

/** Dialog reporting the per-item outcome of a bulk operation. */
export function BulkResultDialog({
  open,
  onOpenChange,
  title,
  description,
  result,
  linkIdentities,
  children,
}: BulkResultDialogProps) {
  const { t } = useTranslation('identities');
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <ListChecksIcon className="size-4 text-muted-foreground" aria-hidden />
            {title}
          </DialogTitle>
          <DialogDescription>{description ?? t('result.description')}</DialogDescription>
        </DialogHeader>
        <BulkResultView result={result} linkIdentities={linkIdentities} />
        {children}
        <DialogFooter>
          <Button onClick={() => onOpenChange(false)}>{t('common:actions.close')}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
