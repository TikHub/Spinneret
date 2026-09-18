import * as LabelPrimitive from '@radix-ui/react-label';
import { forwardRef, type ComponentPropsWithoutRef, type ElementRef } from 'react';

import { cn } from '@/lib/utils';

export const Label = forwardRef<
  ElementRef<typeof LabelPrimitive.Root>,
  ComponentPropsWithoutRef<typeof LabelPrimitive.Root>
>(({ className, ...props }, ref) => (
  <LabelPrimitive.Root
    ref={ref}
    className={cn(
      'flex items-center gap-2 text-sm leading-none font-medium select-none peer-disabled:cursor-not-allowed peer-disabled:opacity-50',
      className,
    )}
    {...props}
  />
));
Label.displayName = LabelPrimitive.Root.displayName;
