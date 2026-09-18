import { LoaderCircleIcon } from 'lucide-react';
import { useId, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { errorMessage } from '@/lib/errors';

export interface ConfirmDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title?: ReactNode;
  description?: ReactNode;
  /** Extra content between description and buttons (reason input, duration, ...). */
  children?: ReactNode;
  confirmLabel?: ReactNode;
  cancelLabel?: ReactNode;
  /** Red confirm button for destructive operations. */
  destructive?: boolean;
  /** Number of affected items shown for bulk actions. */
  affectedCount?: number;
  /** When set, the user must type this exact text before confirming. */
  confirmText?: string;
  /** Disables the confirm button (e.g. invalid extra inputs). */
  confirmDisabled?: boolean;
  /**
   * Called on confirm. When it returns a promise the dialog shows a spinner,
   * closes on success and shows a toast (keeping the dialog open) on failure.
   */
  onConfirm: () => void | Promise<unknown>;
}

/** Confirmation dialog for destructive or sensitive operations. */
export function ConfirmDialog({
  open,
  onOpenChange,
  title,
  description,
  children,
  confirmLabel,
  cancelLabel,
  destructive = false,
  affectedCount,
  confirmText,
  confirmDisabled = false,
  onConfirm,
}: ConfirmDialogProps) {
  const { t } = useTranslation();
  const inputId = useId();
  const [typed, setTyped] = useState('');
  const [pending, setPending] = useState(false);
  const [wasOpen, setWasOpen] = useState(open);
  // Start every opening with an empty confirmation text, also when the parent closes the dialog.
  if (open !== wasOpen) {
    setWasOpen(open);
    if (open) setTyped('');
  }

  const handleOpenChange = (next: boolean) => {
    if (pending) return;
    if (!next) setTyped('');
    onOpenChange(next);
  };

  const confirm = async () => {
    setPending(true);
    try {
      await onConfirm();
      setTyped('');
      onOpenChange(false);
    } catch (err) {
      toast.error(errorMessage(err, t));
    } finally {
      setPending(false);
    }
  };

  const typedOk = confirmText === undefined || typed === confirmText;

  return (
    <AlertDialog open={open} onOpenChange={handleOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{title ?? t('confirm.title')}</AlertDialogTitle>
          {(description || affectedCount !== undefined) && (
            <AlertDialogDescription asChild>
              <div className="space-y-1">
                {description && <p>{description}</p>}
                {affectedCount !== undefined && (
                  <p className="font-medium text-foreground">
                    {t('confirm.affected', { count: affectedCount })}
                  </p>
                )}
              </div>
            </AlertDialogDescription>
          )}
        </AlertDialogHeader>
        {children}
        {confirmText !== undefined && (
          <div className="grid gap-1.5">
            <label htmlFor={inputId} className="text-sm text-muted-foreground">
              {t('confirm.typeToConfirm', { text: confirmText })}
            </label>
            <Input
              id={inputId}
              autoComplete="off"
              spellCheck={false}
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              className="font-mono"
            />
          </div>
        )}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={pending}>{cancelLabel ?? t('actions.cancel')}</AlertDialogCancel>
          <Button
            variant={destructive ? 'destructive' : 'default'}
            disabled={pending || !typedOk || confirmDisabled}
            onClick={() => void confirm()}
          >
            {pending && <LoaderCircleIcon className="animate-spin" />}
            {confirmLabel ?? t('actions.confirm')}
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
