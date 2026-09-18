import { TriangleAlertIcon } from 'lucide-react';
import { useId, useState } from 'react';
import { useTranslation } from 'react-i18next';

import { CopyButton } from '@/components/CopyButton';
import { Button } from '@/components/ui/button';
import { Checkbox } from '@/components/ui/checkbox';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';

/** A freshly created token whose plaintext is shown exactly once. */
export interface CreatedTokenSecret {
  name: string;
  plaintext: string;
}

export interface TokenSecretDialogProps {
  secret: CreatedTokenSecret | undefined;
  /** Called after the user confirmed saving the token; the caller drops the plaintext. */
  onClose: () => void;
}

/**
 * Shows a new token's plaintext once. The dialog cannot be dismissed with
 * Escape or an outside click; closing requires confirming that it was saved.
 */
export function TokenSecretDialog({ secret, onClose }: TokenSecretDialogProps) {
  const { t } = useTranslation('access');
  return (
    // Only the explicit confirmation button closes the dialog, so onOpenChange ignores requests.
    <Dialog open={secret !== undefined} onOpenChange={() => undefined}>
      <DialogContent
        hideClose
        className="sm:max-w-xl"
        onEscapeKeyDown={(event) => event.preventDefault()}
        onPointerDownOutside={(event) => event.preventDefault()}
        onInteractOutside={(event) => event.preventDefault()}
      >
        <DialogHeader>
          <DialogTitle>{t('tokens.secret.title', { name: secret?.name ?? '' })}</DialogTitle>
          <DialogDescription>{t('tokens.secret.description')}</DialogDescription>
        </DialogHeader>
        {secret && <SecretBody secret={secret} onClose={onClose} />}
      </DialogContent>
    </Dialog>
  );
}

function SecretBody({ secret, onClose }: { secret: CreatedTokenSecret; onClose: () => void }) {
  const { t } = useTranslation('access');
  const checkboxId = useId();
  const [saved, setSaved] = useState(false);
  return (
    <>
      <div
        role="alert"
        className="flex gap-2 rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-sm text-amber-800 dark:text-amber-300"
      >
        <TriangleAlertIcon className="mt-0.5 size-4 shrink-0" aria-hidden />
        <p>{t('tokens.secret.warning')}</p>
      </div>
      {/*
        The label sits on the group, not on the <code> element: ARIA forbids
        naming `code`, so an aria-label there is dropped by assistive technology.
      */}
      <div
        role="group"
        aria-label={t('tokens.secret.plaintextLabel')}
        className="flex items-start gap-1 rounded-md border bg-muted/40 p-2"
      >
        <code className="min-w-0 flex-1 px-1 py-0.5 font-mono text-sm break-all select-all">
          {secret.plaintext}
        </code>
        <CopyButton value={secret.plaintext} label={t('tokens.secret.copy')} className="size-7" />
      </div>
      <p className="text-xs text-muted-foreground">
        {t('tokens.secret.usage', { header: 'Authorization: Bearer spn_…' })}
      </p>
      <div className="flex items-center gap-2">
        <Checkbox id={checkboxId} checked={saved} onCheckedChange={(value) => setSaved(value === true)} />
        <label htmlFor={checkboxId} className="text-sm">
          {t('tokens.secret.confirm')}
        </label>
      </div>
      <DialogFooter>
        <Button type="button" onClick={onClose} disabled={!saved}>
          {t('tokens.secret.close')}
        </Button>
      </DialogFooter>
    </>
  );
}
