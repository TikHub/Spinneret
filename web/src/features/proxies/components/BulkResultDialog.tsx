import { useTranslation } from 'react-i18next';

import { IdText } from '@/components/CopyButton';
import { Button } from '@/components/ui/button';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { type BulkFailure } from '@/gen/spinneret/v1/common_pb';
import { isKnownReason } from '@/lib/errors';

import { type BulkSummary } from '../proxyOperations';

export interface BulkResultDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  summary: BulkSummary;
  failures: readonly BulkFailure[];
}

/** Lists the items a bulk operation could not be applied to. */
export function BulkResultDialog({ open, onOpenChange, title, summary, failures }: BulkResultDialogProps) {
  const { t } = useTranslation('proxies');
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{t('bulk.resultSummary', { ...summary })}</DialogDescription>
        </DialogHeader>
        <div className="max-h-80 overflow-auto rounded-md border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t('bulk.proxy')}</TableHead>
                <TableHead>{t('bulk.reason')}</TableHead>
                <TableHead>{t('bulk.message')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {failures.map((failure, index) => (
                <TableRow key={`${failure.id}-${index}`}>
                  <TableCell>
                    <IdText value={failure.id} />
                  </TableCell>
                  <TableCell className="text-xs">
                    {isKnownReason(failure.reason)
                      ? t(`common:errors.reasons.${failure.reason}`, { seconds: 0 })
                      : failure.reason || '—'}
                  </TableCell>
                  <TableCell className="text-xs break-words whitespace-normal text-muted-foreground">
                    {failure.message || '—'}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t('common:actions.close')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
