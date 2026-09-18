import { useTranslation } from 'react-i18next';

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

import { type UnsavedChangesBlocker } from '../useUnsavedChangesGuard';

/** Confirmation shown while a navigation is blocked. */
export function UnsavedChangesDialog({ blocker }: { blocker: UnsavedChangesBlocker }) {
  const { t } = useTranslation('config');
  const open = blocker.status === 'blocked';
  return (
    <AlertDialog
      open={open}
      onOpenChange={(next) => {
        if (!next && blocker.status === 'blocked') blocker.reset();
      }}
    >
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t('unsaved.title')}</AlertDialogTitle>
          <AlertDialogDescription>{t('unsaved.description')}</AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>{t('unsaved.stay')}</AlertDialogCancel>
          <Button
            variant="destructive"
            onClick={() => {
              if (blocker.status === 'blocked') blocker.proceed();
            }}
          >
            {t('unsaved.discard')}
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
