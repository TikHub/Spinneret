import { type VariantProps } from 'class-variance-authority';
import { type HTMLAttributes } from 'react';

import { badgeVariants } from '@/components/ui/variants';
import { cn } from '@/lib/utils';

export interface BadgeProps extends HTMLAttributes<HTMLSpanElement>, VariantProps<typeof badgeVariants> {}

export function Badge({ className, variant, ...props }: BadgeProps) {
  return <span className={cn(badgeVariants({ variant }), className)} {...props} />;
}
