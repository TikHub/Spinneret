import { type ReactNode } from 'react';

import { cn } from '@/lib/utils';

/** Definition list row used by detail cards. */
export function DetailRow({
  label,
  children,
  className,
}: {
  label: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  // The value track may shrink to zero so long identifiers wrap inside the row
  // instead of pushing past the card; below `sm` the label stacks on top.
  return (
    <div
      className={cn(
        'grid grid-cols-1 items-start gap-x-3 gap-y-0.5 py-1.5 text-sm sm:grid-cols-[10rem_minmax(0,1fr)]',
        className,
      )}
    >
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0 wrap-anywhere">{children}</dd>
    </div>
  );
}

/** Divided definition list. */
export function DetailList({ children, className }: { children: ReactNode; className?: string }) {
  return <dl className={cn('divide-y', className)}>{children}</dl>;
}
