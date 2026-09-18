import { LoaderCircleIcon } from 'lucide-react';
import { useState, type ReactNode } from 'react';
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
import { errorMessage } from '@/lib/errors';

export interface WideConfirmDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: ReactNode;
  description?: ReactNode;
  children?: ReactNode;
  confirmLabel: ReactNode;
  destructive?: boolean;
  confirmDisabled?: boolean;
  onConfirm: () => Promise<unknown>;
}

/**
 * ConfirmDialog behavior (spinner, closes on success, toast and stays open on
 * failure) in a wide layout for diff previews. The shared ConfirmDialog has a
 * fixed small width.
 */
export function WideConfirmDialog({
  open,
  onOpenChange,
  title,
  description,
  children,
  confirmLabel,
  destructive = false,
  confirmDisabled = false,
  onConfirm,
}: WideConfirmDialogProps) {
  const { t } = useTranslation();
  const [pending, setPending] = useState(false);

  const confirm = async () => {
    setPending(true);
    try {
      await onConfirm();
      onOpenChange(false);
    } catch (err) {
      toast.error(errorMessage(err, t));
    } finally {
      setPending(false);
    }
  };

  return (
    <AlertDialog open={open} onOpenChange={(next) => !pending && onOpenChange(next)}>
      <AlertDialogContent className="max-h-[calc(100vh-2rem)] overflow-y-auto sm:max-w-5xl">
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          {description && (
            <AlertDialogDescription asChild>
              <div>{description}</div>
            </AlertDialogDescription>
          )}
        </AlertDialogHeader>
        {children}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={pending}>{t('actions.cancel')}</AlertDialogCancel>
          <Button
            variant={destructive ? 'destructive' : 'default'}
            disabled={pending || confirmDisabled}
            onClick={() => void confirm()}
          >
            {pending && <LoaderCircleIcon className="animate-spin" />}
            {confirmLabel}
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
