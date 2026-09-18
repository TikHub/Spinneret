import { cloneElement, isValidElement, useId, type ReactElement, type ReactNode } from 'react';

import { Label } from '@/components/ui/label';
import { cn } from '@/lib/utils';

interface ControlProps {
  id?: string;
  'aria-invalid'?: boolean;
  'aria-describedby'?: string;
}

export interface FormFieldProps {
  label?: ReactNode;
  /** Help text below the control. */
  description?: ReactNode;
  /** Error message; marks the control aria-invalid. */
  error?: ReactNode;
  required?: boolean;
  /** Explicit control id; generated when omitted. */
  id?: string;
  className?: string;
  /** Label on the left of the control instead of above it (switches, checkboxes). */
  inline?: boolean;
  /** A single control element; id, aria-invalid and aria-describedby are injected. */
  children: ReactElement<ControlProps>;
}

/**
 * Form field wrapper: label, control, description and error with accessible
 * wiring. Works with plain useState forms (no form library required).
 */
export function FormField({
  label,
  description,
  error,
  required,
  id,
  className,
  inline = false,
  children,
}: FormFieldProps) {
  const generatedId = useId();
  const controlId = id ?? children.props.id ?? generatedId;
  const descriptionId = description && !error ? `${controlId}-description` : undefined;
  const errorId = error ? `${controlId}-error` : undefined;
  const describedBy = [descriptionId, errorId].filter(Boolean).join(' ') || undefined;

  const control = isValidElement(children)
    ? cloneElement(children, {
        id: controlId,
        'aria-invalid': error ? true : children.props['aria-invalid'],
        'aria-describedby': describedBy,
      })
    : children;

  return (
    <div className={cn(inline ? 'flex items-center gap-3' : 'grid content-start gap-1.5', className)}>
      {inline && control}
      {label && (
        <Label htmlFor={controlId}>
          {label}
          {required && <span className="text-destructive">*</span>}
        </Label>
      )}
      {!inline && control}
      {descriptionId && (
        <p id={descriptionId} className="text-xs text-muted-foreground">
          {description}
        </p>
      )}
      {error && (
        <p id={errorId} role="alert" className="text-xs text-destructive">
          {error}
        </p>
      )}
    </div>
  );
}

/** Vertical stack for form fields. */
export function FormStack({ className, children }: { className?: string; children: ReactNode }) {
  return <div className={cn('grid gap-4', className)}>{children}</div>;
}
