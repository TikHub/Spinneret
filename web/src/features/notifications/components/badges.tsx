import {
  BellRingIcon,
  BirdIcon,
  MessagesSquareIcon,
  SendIcon,
  WebhookIcon,
  type LucideIcon,
} from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { cn } from '@/lib/utils';

const KIND_ICONS: Record<string, LucideIcon> = {
  webhook: WebhookIcon,
  feishu: BirdIcon,
  dingtalk: BellRingIcon,
  wecom: MessagesSquareIcon,
  telegram: SendIcon,
};

/** Channel kind icon and label. */
export function ChannelKindLabel({ kind, className }: { kind: string; className?: string }) {
  const { t } = useTranslation('notifications');
  const Icon = KIND_ICONS[kind] ?? WebhookIcon;
  return (
    <span className={cn('inline-flex items-center gap-1.5', className)}>
      <Icon className="size-4 shrink-0 text-muted-foreground" aria-hidden />
      {t(`kinds.${kind}`, { defaultValue: kind })}
    </span>
  );
}

const SEVERITY_CLASSES: Record<string, string> = {
  info: 'border-sky-500/25 bg-sky-500/10 text-sky-700 dark:text-sky-400',
  warning: 'border-amber-500/25 bg-amber-500/10 text-amber-700 dark:text-amber-400',
  critical: 'border-rose-500/25 bg-rose-500/10 text-rose-700 dark:text-rose-400',
};

/** Severity pill (info = sky, warning = amber, critical = rose). */
export function SeverityBadge({ severity, className }: { severity: string; className?: string }) {
  const { t } = useTranslation('notifications');
  return (
    <span
      className={cn(
        'inline-flex w-fit items-center rounded-full border px-2 py-0.5 text-xs font-medium whitespace-nowrap',
        SEVERITY_CLASSES[severity] ?? 'border-zinc-500/25 bg-zinc-500/10 text-zinc-600 dark:text-zinc-400',
        className,
      )}
    >
      {t(`severities.${severity || 'warning'}`, { defaultValue: severity })}
    </span>
  );
}
