import { type LucideIcon } from 'lucide-react';
import { type ReactNode } from 'react';

import { Card } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import { cn } from '@/lib/utils';

const TONE_CLASSES = {
  default: 'text-foreground',
  success: 'text-emerald-600 dark:text-emerald-400',
  warning: 'text-amber-600 dark:text-amber-400',
  danger: 'text-rose-600 dark:text-rose-400',
  muted: 'text-muted-foreground',
} as const;

export type StatTone = keyof typeof TONE_CLASSES;

export interface StatCardProps {
  label: ReactNode;
  value: ReactNode;
  /** Secondary line under the value. */
  hint?: ReactNode;
  icon?: LucideIcon;
  tone?: StatTone;
  loading?: boolean;
  className?: string;
  /** Extra content at the bottom (sparkline, badges). */
  children?: ReactNode;
}

/** Compact metric tile. */
export function StatCard({
  label,
  value,
  hint,
  icon: Icon,
  tone = 'default',
  loading,
  className,
  children,
}: StatCardProps) {
  return (
    <Card className={cn('gap-1 p-4', className)}>
      <div className="flex items-center justify-between gap-2">
        <span className="truncate text-xs font-medium text-muted-foreground">{label}</span>
        {Icon && <Icon className="size-4 shrink-0 text-muted-foreground" aria-hidden />}
      </div>
      {loading ? (
        <Skeleton className="mt-1 h-7 w-20" />
      ) : (
        <div className={cn('tabular text-2xl leading-tight font-semibold', TONE_CLASSES[tone])}>{value}</div>
      )}
      {hint && <div className="truncate text-xs text-muted-foreground">{hint}</div>}
      {children}
    </Card>
  );
}
