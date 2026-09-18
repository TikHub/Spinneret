import * as SelectPrimitive from '@radix-ui/react-select';
import { CheckIcon, ChevronDownIcon, ChevronUpIcon } from 'lucide-react';
import { createContext, forwardRef, useContext, type ComponentPropsWithoutRef, type ElementRef } from 'react';

import { cn } from '@/lib/utils';

/** Field wiring that FormField injects into its child control. */
interface SelectFieldProps {
  id?: string;
  'aria-invalid'?: boolean;
  'aria-describedby'?: string;
}

const SelectFieldContext = createContext<SelectFieldProps>({});

export type SelectProps = ComponentPropsWithoutRef<typeof SelectPrimitive.Root> & SelectFieldProps;

/**
 * Select root. `id`, `aria-invalid` and `aria-describedby` (as injected by
 * FormField) are forwarded to SelectTrigger so the label and errors are
 * associated with the focusable trigger.
 */
export function Select({
  id,
  'aria-invalid': ariaInvalid,
  'aria-describedby': ariaDescribedBy,
  ...props
}: SelectProps) {
  return (
    <SelectFieldContext.Provider
      value={{ id, 'aria-invalid': ariaInvalid, 'aria-describedby': ariaDescribedBy }}
    >
      <SelectPrimitive.Root {...props} />
    </SelectFieldContext.Provider>
  );
}

export const SelectGroup = SelectPrimitive.Group;

/**
 * Selected value (or placeholder). Radix drops className and style here, so the
 * trigger styles the value through `data-slot="select-value"`; without the
 * attribute a long value or placeholder overflows the trigger instead of being
 * clamped to one line.
 */
export const SelectValue = forwardRef<
  ElementRef<typeof SelectPrimitive.Value>,
  ComponentPropsWithoutRef<typeof SelectPrimitive.Value>
>((props, ref) => <SelectPrimitive.Value ref={ref} data-slot="select-value" {...props} />);
SelectValue.displayName = 'SelectValue';

export const SelectTrigger = forwardRef<
  ElementRef<typeof SelectPrimitive.Trigger>,
  ComponentPropsWithoutRef<typeof SelectPrimitive.Trigger> & { size?: 'sm' | 'default' }
>(({ className, children, size = 'default', ...props }, ref) => {
  const field = useContext(SelectFieldContext);
  return (
    <SelectPrimitive.Trigger
      ref={ref}
      data-size={size}
      id={field.id}
      aria-invalid={field['aria-invalid']}
      aria-describedby={field['aria-describedby']}
      className={cn(
        'flex w-fit items-center justify-between gap-2 rounded-md border border-input bg-background px-3 text-sm whitespace-nowrap shadow-xs transition-colors outline-none focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50 aria-invalid:border-destructive data-[placeholder]:text-muted-foreground data-[size=default]:h-9 data-[size=sm]:h-8 *:data-[slot=select-value]:line-clamp-1 *:data-[slot=select-value]:flex *:data-[slot=select-value]:items-center *:data-[slot=select-value]:gap-2 [&_svg]:pointer-events-none [&_svg]:shrink-0',
        className,
      )}
      {...props}
    >
      {children}
      <SelectPrimitive.Icon asChild>
        <ChevronDownIcon className="size-4 opacity-50" />
      </SelectPrimitive.Icon>
    </SelectPrimitive.Trigger>
  );
});
SelectTrigger.displayName = SelectPrimitive.Trigger.displayName;

export const SelectContent = forwardRef<
  ElementRef<typeof SelectPrimitive.Content>,
  ComponentPropsWithoutRef<typeof SelectPrimitive.Content>
>(({ className, children, position = 'popper', ...props }, ref) => (
  <SelectPrimitive.Portal>
    <SelectPrimitive.Content
      ref={ref}
      position={position}
      className={cn(
        'relative z-50 max-h-(--radix-select-content-available-height) min-w-[8rem] overflow-x-hidden overflow-y-auto rounded-md border bg-popover text-popover-foreground shadow-md animate-fade-in',
        position === 'popper' &&
          'data-[side=bottom]:translate-y-1 data-[side=left]:-translate-x-1 data-[side=right]:translate-x-1 data-[side=top]:-translate-y-1',
        className,
      )}
      {...props}
    >
      <SelectPrimitive.ScrollUpButton className="flex cursor-default items-center justify-center py-1">
        <ChevronUpIcon className="size-4" />
      </SelectPrimitive.ScrollUpButton>
      <SelectPrimitive.Viewport
        className={cn(
          'p-1',
          position === 'popper' &&
            'h-(--radix-select-trigger-height) w-full min-w-(--radix-select-trigger-width) scroll-my-1',
        )}
      >
        {children}
      </SelectPrimitive.Viewport>
      <SelectPrimitive.ScrollDownButton className="flex cursor-default items-center justify-center py-1">
        <ChevronDownIcon className="size-4" />
      </SelectPrimitive.ScrollDownButton>
    </SelectPrimitive.Content>
  </SelectPrimitive.Portal>
));
SelectContent.displayName = SelectPrimitive.Content.displayName;

export const SelectLabel = forwardRef<
  ElementRef<typeof SelectPrimitive.Label>,
  ComponentPropsWithoutRef<typeof SelectPrimitive.Label>
>(({ className, ...props }, ref) => (
  <SelectPrimitive.Label
    ref={ref}
    className={cn('px-2 py-1.5 text-xs text-muted-foreground', className)}
    {...props}
  />
));
SelectLabel.displayName = SelectPrimitive.Label.displayName;

export const SelectItem = forwardRef<
  ElementRef<typeof SelectPrimitive.Item>,
  ComponentPropsWithoutRef<typeof SelectPrimitive.Item>
>(({ className, children, ...props }, ref) => (
  <SelectPrimitive.Item
    ref={ref}
    className={cn(
      'relative flex w-full cursor-default items-center gap-2 rounded-sm py-1.5 pr-8 pl-2 text-sm outline-hidden select-none focus:bg-accent focus:text-accent-foreground data-[disabled]:pointer-events-none data-[disabled]:opacity-50 [&_svg]:pointer-events-none [&_svg]:shrink-0',
      className,
    )}
    {...props}
  >
    <span className="absolute right-2 flex size-3.5 items-center justify-center">
      <SelectPrimitive.ItemIndicator>
        <CheckIcon className="size-4" />
      </SelectPrimitive.ItemIndicator>
    </span>
    <SelectPrimitive.ItemText>{children}</SelectPrimitive.ItemText>
  </SelectPrimitive.Item>
));
SelectItem.displayName = SelectPrimitive.Item.displayName;

export const SelectSeparator = forwardRef<
  ElementRef<typeof SelectPrimitive.Separator>,
  ComponentPropsWithoutRef<typeof SelectPrimitive.Separator>
>(({ className, ...props }, ref) => (
  <SelectPrimitive.Separator ref={ref} className={cn('-mx-1 my-1 h-px bg-border', className)} {...props} />
));
SelectSeparator.displayName = SelectPrimitive.Separator.displayName;
