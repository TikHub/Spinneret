import { useTranslation } from 'react-i18next';

import { SimpleTooltip } from '@/components/ui/tooltip';
import { type SseStatus } from '@/lib/sse';
import { cn } from '@/lib/utils';

const DOT: Record<SseStatus, string> = {
  open: 'bg-emerald-500',
  connecting: 'bg-sky-500 animate-pulse',
  reconnecting: 'bg-amber-500 animate-pulse',
  idle: 'bg-zinc-400',
};

/** Small dot showing the state of the real-time event stream. */
export function LiveIndicator({ status }: { status: SseStatus }) {
  const { t } = useTranslation();
  const label = t(`shell.live.${status}`);
  return (
    <SimpleTooltip content={label}>
      <span className="flex size-7 items-center justify-center" role="status" aria-label={label} tabIndex={0}>
        <span className={cn('size-2 rounded-full', DOT[status])} />
      </span>
    </SimpleTooltip>
  );
}
