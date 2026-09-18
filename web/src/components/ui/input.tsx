import { forwardRef, type InputHTMLAttributes } from 'react';

import { cn } from '@/lib/utils';

export const inputClassName =
  'flex h-9 w-full min-w-0 rounded-md border border-input bg-background px-3 py-1 text-sm shadow-xs transition-colors outline-none placeholder:text-muted-foreground file:border-0 file:bg-transparent file:text-sm file:font-medium focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50 aria-invalid:border-destructive aria-invalid:ring-destructive/20';

export const Input = forwardRef<HTMLInputElement, InputHTMLAttributes<HTMLInputElement>>(
  ({ className, type, ...props }, ref) => (
    <input ref={ref} type={type} className={cn(inputClassName, className)} {...props} />
  ),
);
Input.displayName = 'Input';
