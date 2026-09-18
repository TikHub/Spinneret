import { ActivityIcon } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { cn } from '@/lib/utils';

import { type PolicyKind } from '../constants';
import { KIND_ICONS } from '../selectors';

const KIND_TONES: Readonly<Record<PolicyKind, string>> = {
  rotation: 'border-indigo-500/25 bg-indigo-500/10 text-indigo-700 dark:text-indigo-300',
  signal: 'border-sky-500/25 bg-sky-500/10 text-sky-700 dark:text-sky-300',
  action: 'border-orange-500/25 bg-orange-500/10 text-orange-700 dark:text-orange-300',
  breaker: 'border-rose-500/25 bg-rose-500/10 text-rose-700 dark:text-rose-300',
};

/** Colored badge with the icon and translated name of a policy kind. */
export function KindBadge({ kind, className }: { kind: string; className?: string }) {
  const { t } = useTranslation('policies');
  const known = kind in KIND_ICONS;
  const Icon = known ? KIND_ICONS[kind as PolicyKind] : ActivityIcon;
  return (
    <span
      className={cn(
        'inline-flex w-fit items-center gap-1 rounded-md border px-1.5 py-0.5 text-xs font-medium whitespace-nowrap',
        known ? KIND_TONES[kind as PolicyKind] : 'border-border text-muted-foreground',
        className,
      )}
    >
      <Icon className="size-3" aria-hidden />
      {t(`kinds.${kind}`, { defaultValue: kind })}
    </span>
  );
}

/** Badge of a binding or resolution level. */
export function LevelBadge({ level }: { level: string }) {
  const { t } = useTranslation('policies');
  return (
    <span className="inline-flex w-fit items-center rounded-md border bg-muted/50 px-1.5 py-0.5 text-xs whitespace-nowrap text-muted-foreground">
      {t(`levels.${level}`, { defaultValue: level })}
    </span>
  );
}
