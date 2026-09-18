import { useState } from 'react';
import { useTranslation } from 'react-i18next';

import { ConfirmDialog } from '@/components/ConfirmDialog';
import { DurationInput } from '@/components/DurationInput';
import { FormField } from '@/components/ui/form';
import { Textarea } from '@/components/ui/textarea';
import { type BreakerStatus } from '@/gen/spinneret/v1/breaker_admin_pb';
import { validateDurationInput } from '@/lib/duration';
import { cn } from '@/lib/utils';

import { useCloseBreaker, useOpenBreaker } from '../useBreakers';

const OPEN_PRESETS = ['5m', '10m', '30m', '1h', '6h', '1d'] as const;
const REASON_MAX = 1024;

export interface BreakerDialogProps {
  breaker: BreakerStatus | null;
  onOpenChange: (open: boolean) => void;
}

function target(b: BreakerStatus): string {
  return `${b.site} / ${b.client} / ${b.endpointGroup}`;
}

/** Manually opens a breaker for a duration or indefinitely. */
export function OpenBreakerDialog({ breaker, onOpenChange }: BreakerDialogProps) {
  const { t } = useTranslation('breakers');
  const open = useOpenBreaker();
  const [mode, setMode] = useState<'duration' | 'indefinite'>('duration');
  const [duration, setDuration] = useState('30m');
  const [reason, setReason] = useState('');
  const [lastId, setLastId] = useState<string | undefined>();
  const id = breaker?.endpointGroupId;
  if (id !== lastId) {
    setLastId(id);
    if (id) {
      setMode('duration');
      setDuration('30m');
      setReason('');
    }
  }
  const durationOk = mode === 'indefinite' || validateDurationInput(duration) === 'ok';

  return (
    <ConfirmDialog
      open={breaker !== null}
      onOpenChange={onOpenChange}
      destructive
      title={t('open.title')}
      description={breaker ? t('open.description', { target: target(breaker) }) : undefined}
      confirmLabel={t('open.confirm')}
      confirmDisabled={!durationOk}
      onConfirm={() =>
        breaker
          ? open.mutateAsync({
              endpointGroupId: breaker.endpointGroupId,
              duration: mode === 'indefinite' ? '' : duration.trim(),
              reason: reason.trim(),
            })
          : undefined
      }
    >
      <div className="grid gap-3">
        <fieldset className="grid gap-2">
          <legend className="mb-1 text-sm font-medium">{t('open.period')}</legend>
          <div role="radiogroup" className="inline-flex w-fit rounded-md border p-0.5">
            {(['duration', 'indefinite'] as const).map((m) => (
              <button
                key={m}
                type="button"
                role="radio"
                aria-checked={mode === m}
                onClick={() => setMode(m)}
                className={cn(
                  'rounded px-3 py-1 text-sm outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50',
                  mode === m ? 'bg-secondary font-medium' : 'text-muted-foreground hover:text-foreground',
                )}
              >
                {t(`open.modes.${m}`)}
              </button>
            ))}
          </div>
        </fieldset>
        {mode === 'duration' ? (
          <FormField label={t('open.duration')} required>
            <DurationInput value={duration} onChange={setDuration} presets={OPEN_PRESETS} />
          </FormField>
        ) : (
          <p className="text-sm text-muted-foreground">{t('open.indefiniteHint')}</p>
        )}
        <FormField label={t('reason')} description={t('reasonHint')}>
          <Textarea
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            rows={2}
            maxLength={REASON_MAX}
          />
        </FormField>
      </div>
    </ConfirmDialog>
  );
}

/** Manually closes a breaker. */
export function CloseBreakerDialog({ breaker, onOpenChange }: BreakerDialogProps) {
  const { t } = useTranslation('breakers');
  const close = useCloseBreaker();
  const [reason, setReason] = useState('');
  const [lastId, setLastId] = useState<string | undefined>();
  const id = breaker?.endpointGroupId;
  if (id !== lastId) {
    setLastId(id);
    if (id) setReason('');
  }

  return (
    <ConfirmDialog
      open={breaker !== null}
      onOpenChange={onOpenChange}
      title={t('close.title')}
      description={breaker ? t('close.description', { target: target(breaker) }) : undefined}
      confirmLabel={t('close.confirm')}
      onConfirm={() =>
        breaker
          ? close.mutateAsync({ endpointGroupId: breaker.endpointGroupId, reason: reason.trim() })
          : undefined
      }
    >
      <FormField label={t('reason')} description={t('reasonHint')}>
        <Textarea
          value={reason}
          onChange={(e) => setReason(e.target.value)}
          rows={2}
          maxLength={REASON_MAX}
        />
      </FormField>
    </ConfirmDialog>
  );
}
