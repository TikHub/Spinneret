import { type ReactNode } from 'react';

import { cn } from '@/lib/utils';

export interface DetailItem {
  key: string;
  label: ReactNode;
  value: ReactNode;
  /** Puts the label above a full-width value (URIs, JSON, free text). */
  wide?: boolean;
}

/**
 * Definition list of event fields.
 *
 * Every row is a two track grid: a fixed label column and a value column that
 * may shrink to zero (`minmax(0,1fr)`). A value that cannot be broken — a
 * 40-character identifier, a URI with a query string — then wraps inside its own
 * row instead of keeping its intrinsic width and painting over the next field.
 * Below `sm` the drawer is full width and narrow, so the label stacks on top.
 */
export function DetailGrid({ items, className }: { items: readonly DetailItem[]; className?: string }) {
  return (
    <dl className={cn('grid grid-cols-1 divide-y divide-border/60 text-sm', className)}>
      {items.map((item) => (
        <div
          key={item.key}
          className={cn(
            'grid min-w-0 gap-x-4 gap-y-0.5 py-1.5',
            !item.wide && 'sm:grid-cols-[minmax(0,9rem)_minmax(0,1fr)] sm:items-baseline',
          )}
        >
          <dt className="text-xs text-muted-foreground">{item.label}</dt>
          <dd className="min-w-0 wrap-anywhere">{item.value}</dd>
        </div>
      ))}
    </dl>
  );
}
