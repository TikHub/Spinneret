import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
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

import {
  createUnsavedChangesRegistry,
  UnsavedChangesContext,
  type UnsavedChangesContextValue,
} from './registry';

/**
 * Holds the unsaved-changes registry of the console and renders the
 * confirmation shown before tenant or namespace switches discard edits.
 */
export function UnsavedChangesProvider({ children }: { children: ReactNode }) {
  const { t } = useTranslation();
  const [registry] = useState(createUnsavedChangesRegistry);
  const [open, setOpen] = useState(false);
  const resolver = useRef<((discard: boolean) => void) | undefined>(undefined);

  const settle = useCallback((discard: boolean) => {
    const resolve = resolver.current;
    resolver.current = undefined;
    setOpen(false);
    resolve?.(discard);
  }, []);

  const confirmDiscard = useCallback(() => {
    if (!registry.isDirty()) return Promise.resolve(true);
    // A newer request replaces a pending one, which keeps the changes.
    resolver.current?.(false);
    return new Promise<boolean>((resolve) => {
      resolver.current = resolve;
      setOpen(true);
    });
  }, [registry]);

  // A pending confirmation never outlives the provider.
  useEffect(
    () => () => {
      resolver.current?.(false);
      resolver.current = undefined;
    },
    [],
  );

  const value = useMemo<UnsavedChangesContextValue>(
    () => ({ registry, confirmDiscard }),
    [registry, confirmDiscard],
  );

  return (
    <UnsavedChangesContext.Provider value={value}>
      {children}
      <AlertDialog
        open={open}
        onOpenChange={(next) => {
          if (!next) settle(false);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t('unsaved.title')}</AlertDialogTitle>
            <AlertDialogDescription>{t('unsaved.scopeSwitchDescription')}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t('unsaved.stay')}</AlertDialogCancel>
            <Button variant="destructive" onClick={() => settle(true)}>
              {t('unsaved.discard')}
            </Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </UnsavedChangesContext.Provider>
  );
}
