import { EyeOffIcon, TimerIcon } from 'lucide-react';
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

import { useRevealCountdown, type RevealTimer } from '../useRevealTimer';

export interface RevealedSecretDialogProps {
  path: string;
  timer: RevealTimer;
}

/** Shows a revealed value with copy for a limited time; closing or the countdown drops it. */
export function RevealedSecretDialog({ path, timer }: RevealedSecretDialogProps) {
  const { t } = useTranslation('secrets');
  const { revealed, hideAt, durationMs, hide } = timer;
  // The countdown ticks here, not in the page holding the timer.
  const secondsLeft = useRevealCountdown(hideAt, durationMs);
  const total = Math.max(1, Math.ceil(durationMs / 1000));
  return (
    <Dialog open={revealed !== undefined} onOpenChange={(open) => !open && hide()}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{t('revealed.title', { path, version: revealed?.version ?? 0 })}</DialogTitle>
          <DialogDescription>{t('revealed.description')}</DialogDescription>
        </DialogHeader>
        {revealed && (
          <div className="grid gap-2">
            <div className="relative">
              <pre
                className="max-h-72 overflow-auto rounded-md border bg-muted/40 p-3 pr-10 font-mono text-xs break-all whitespace-pre-wrap"
                aria-label={t('revealed.valueLabel')}
              >
                {revealed.value}
              </pre>
              <CopyButton
                value={revealed.value}
                className="absolute top-2 right-2"
                label={t('revealed.copy')}
              />
            </div>
            {/* No live region: announcing the countdown every second would flood screen readers. */}
            <div className="flex items-center gap-2 text-xs text-muted-foreground">
              <TimerIcon className="size-3.5" aria-hidden />
              <span className="tabular">{t('revealed.hidesIn', { count: secondsLeft })}</span>
              <div
                className="h-1 flex-1 overflow-hidden rounded-full bg-muted"
                role="progressbar"
                aria-valuemin={0}
                aria-valuemax={total}
                aria-valuenow={secondsLeft}
                aria-label={t('revealed.hidesIn', { count: secondsLeft })}
              >
                <div
                  className="h-full bg-amber-500 transition-[width] duration-300"
                  style={{ width: `${(secondsLeft / total) * 100}%` }}
                />
              </div>
            </div>
          </div>
        )}
        <DialogFooter>
          <Button variant="outline" onClick={hide}>
            <EyeOffIcon />
            {t('revealed.hide')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
