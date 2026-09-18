import * as SheetPrimitive from '@radix-ui/react-dialog';
import { XIcon } from 'lucide-react';
import { forwardRef, type ComponentPropsWithoutRef, type ElementRef, type HTMLAttributes } from 'react';
import { useTranslation } from 'react-i18next';

import { cn } from '@/lib/utils';

/** Screen-reader label of the close (X) button; translated unless overridden. */
function CloseLabel({ label }: { label?: string }) {
  const { t } = useTranslation();
  return <span className="sr-only">{label ?? t('actions.close')}</span>;
}

export const Sheet = SheetPrimitive.Root;
export const SheetTrigger = SheetPrimitive.Trigger;
export const SheetClose = SheetPrimitive.Close;

export const SheetOverlay = forwardRef<
  ElementRef<typeof SheetPrimitive.Overlay>,
  ComponentPropsWithoutRef<typeof SheetPrimitive.Overlay>
>(({ className, ...props }, ref) => (
  <SheetPrimitive.Overlay
    ref={ref}
    className={cn('fixed inset-0 z-50 bg-black/40 data-[state=open]:animate-fade-in', className)}
    {...props}
  />
));
SheetOverlay.displayName = SheetPrimitive.Overlay.displayName;

const sideClassNames = {
  right: 'inset-y-0 right-0 h-full w-3/4 border-l sm:max-w-md data-[state=open]:animate-sheet-in-right',
  left: 'inset-y-0 left-0 h-full w-3/4 border-r sm:max-w-md',
  top: 'inset-x-0 top-0 h-auto border-b',
  bottom: 'inset-x-0 bottom-0 h-auto border-t',
} as const;

export interface SheetContentProps extends ComponentPropsWithoutRef<typeof SheetPrimitive.Content> {
  side?: keyof typeof sideClassNames;
  closeLabel?: string;
}

export const SheetContent = forwardRef<ElementRef<typeof SheetPrimitive.Content>, SheetContentProps>(
  ({ side = 'right', className, children, closeLabel, ...props }, ref) => (
    <SheetPrimitive.Portal>
      <SheetOverlay />
      <SheetPrimitive.Content
        ref={ref}
        className={cn(
          'fixed z-50 flex flex-col gap-4 overflow-y-auto bg-background shadow-lg outline-none',
          sideClassNames[side],
          className,
        )}
        {...props}
      >
        {children}
        <SheetPrimitive.Close className="absolute top-4 right-4 rounded-xs opacity-70 transition-opacity hover:opacity-100 focus:ring-2 focus:ring-ring focus:outline-hidden [&_svg]:size-4">
          <XIcon />
          <CloseLabel label={closeLabel} />
        </SheetPrimitive.Close>
      </SheetPrimitive.Content>
    </SheetPrimitive.Portal>
  ),
);
SheetContent.displayName = SheetPrimitive.Content.displayName;

export function SheetHeader({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn('flex flex-col gap-1.5 p-4 pr-10', className)} {...props} />;
}

export function SheetFooter({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn('mt-auto flex flex-col gap-2 p-4', className)} {...props} />;
}

export const SheetTitle = forwardRef<
  ElementRef<typeof SheetPrimitive.Title>,
  ComponentPropsWithoutRef<typeof SheetPrimitive.Title>
>(({ className, ...props }, ref) => (
  <SheetPrimitive.Title
    ref={ref}
    className={cn('text-base font-semibold text-foreground', className)}
    {...props}
  />
));
SheetTitle.displayName = SheetPrimitive.Title.displayName;

export const SheetDescription = forwardRef<
  ElementRef<typeof SheetPrimitive.Description>,
  ComponentPropsWithoutRef<typeof SheetPrimitive.Description>
>(({ className, ...props }, ref) => (
  <SheetPrimitive.Description
    ref={ref}
    className={cn('text-sm text-muted-foreground', className)}
    {...props}
  />
));
SheetDescription.displayName = SheetPrimitive.Description.displayName;
