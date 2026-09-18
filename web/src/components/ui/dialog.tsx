import * as DialogPrimitive from '@radix-ui/react-dialog';
import { XIcon } from 'lucide-react';
import { forwardRef, type ComponentPropsWithoutRef, type ElementRef, type HTMLAttributes } from 'react';
import { useTranslation } from 'react-i18next';

import { cn } from '@/lib/utils';

/** Screen-reader label of the close (X) button; translated unless overridden. */
function CloseLabel({ label }: { label?: string }) {
  const { t } = useTranslation();
  return <span className="sr-only">{label ?? t('actions.close')}</span>;
}

export const Dialog = DialogPrimitive.Root;
export const DialogTrigger = DialogPrimitive.Trigger;
export const DialogPortal = DialogPrimitive.Portal;
export const DialogClose = DialogPrimitive.Close;

export const DialogOverlay = forwardRef<
  ElementRef<typeof DialogPrimitive.Overlay>,
  ComponentPropsWithoutRef<typeof DialogPrimitive.Overlay>
>(({ className, ...props }, ref) => (
  <DialogPrimitive.Overlay
    ref={ref}
    className={cn('fixed inset-0 z-50 bg-black/50 data-[state=open]:animate-fade-in', className)}
    {...props}
  />
));
DialogOverlay.displayName = DialogPrimitive.Overlay.displayName;

export interface DialogContentProps extends ComponentPropsWithoutRef<typeof DialogPrimitive.Content> {
  /** Hide the close (X) button in the top-right corner. */
  hideClose?: boolean;
  /** Screen-reader label of the close button (default: translated "Close"). */
  closeLabel?: string;
}

export const DialogContent = forwardRef<ElementRef<typeof DialogPrimitive.Content>, DialogContentProps>(
  ({ className, children, hideClose = false, closeLabel, ...props }, ref) => (
    <DialogPortal>
      <DialogOverlay />
      <DialogPrimitive.Content
        ref={ref}
        className={cn(
          'fixed top-[50%] left-[50%] z-50 grid max-h-[calc(100vh-2rem)] w-full max-w-[calc(100%-2rem)] translate-x-[-50%] translate-y-[-50%] gap-4 overflow-y-auto rounded-lg border bg-background p-6 shadow-lg outline-none data-[state=open]:animate-dialog-in sm:max-w-lg',
          className,
        )}
        {...props}
      >
        {children}
        {!hideClose && (
          <DialogPrimitive.Close className="absolute top-4 right-4 rounded-xs opacity-70 transition-opacity hover:opacity-100 focus:ring-2 focus:ring-ring focus:outline-hidden disabled:pointer-events-none [&_svg]:size-4">
            <XIcon />
            <CloseLabel label={closeLabel} />
          </DialogPrimitive.Close>
        )}
      </DialogPrimitive.Content>
    </DialogPortal>
  ),
);
DialogContent.displayName = DialogPrimitive.Content.displayName;

export function DialogHeader({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn('flex flex-col gap-1.5 text-left', className)} {...props} />;
}

export function DialogFooter({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return (
    <div className={cn('flex flex-col-reverse gap-2 sm:flex-row sm:justify-end', className)} {...props} />
  );
}

export const DialogTitle = forwardRef<
  ElementRef<typeof DialogPrimitive.Title>,
  ComponentPropsWithoutRef<typeof DialogPrimitive.Title>
>(({ className, ...props }, ref) => (
  <DialogPrimitive.Title
    ref={ref}
    className={cn('text-base leading-none font-semibold', className)}
    {...props}
  />
));
DialogTitle.displayName = DialogPrimitive.Title.displayName;

export const DialogDescription = forwardRef<
  ElementRef<typeof DialogPrimitive.Description>,
  ComponentPropsWithoutRef<typeof DialogPrimitive.Description>
>(({ className, ...props }, ref) => (
  <DialogPrimitive.Description
    ref={ref}
    className={cn('text-sm text-muted-foreground', className)}
    {...props}
  />
));
DialogDescription.displayName = DialogPrimitive.Description.displayName;
