import * as TooltipPrimitive from '@radix-ui/react-tooltip';
import { forwardRef, type ComponentPropsWithoutRef, type ElementRef, type ReactNode } from 'react';

import { cn } from '@/lib/utils';

export const TooltipProvider = TooltipPrimitive.Provider;
export const Tooltip = TooltipPrimitive.Root;
export const TooltipTrigger = TooltipPrimitive.Trigger;

export const TooltipContent = forwardRef<
  ElementRef<typeof TooltipPrimitive.Content>,
  ComponentPropsWithoutRef<typeof TooltipPrimitive.Content>
>(({ className, sideOffset = 4, ...props }, ref) => (
  <TooltipPrimitive.Portal>
    <TooltipPrimitive.Content
      ref={ref}
      sideOffset={sideOffset}
      className={cn(
        'z-50 max-w-xs rounded-md bg-foreground px-2.5 py-1.5 text-xs text-background shadow-md animate-fade-in',
        className,
      )}
      {...props}
    />
  </TooltipPrimitive.Portal>
));
TooltipContent.displayName = TooltipPrimitive.Content.displayName;

export interface SimpleTooltipProps {
  content: ReactNode;
  children: ReactNode;
  side?: 'top' | 'right' | 'bottom' | 'left';
  /** When false the children render without a tooltip. */
  enabled?: boolean;
}

/** Wraps a single focusable child with a tooltip. Wrap disabled buttons in a <span> so hover works. */
export function SimpleTooltip({ content, children, side = 'top', enabled = true }: SimpleTooltipProps) {
  if (!enabled || content === null || content === undefined || content === '') return <>{children}</>;
  return (
    <Tooltip>
      <TooltipTrigger asChild>{children}</TooltipTrigger>
      <TooltipContent side={side}>{content}</TooltipContent>
    </Tooltip>
  );
}
