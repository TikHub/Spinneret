import { type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';

import { DurationInput } from '@/components/DurationInput';
import { TagsInput } from '@/components/TagsInput';
import { Checkbox } from '@/components/ui/checkbox';
import { FormField } from '@/components/ui/form';
import { Input } from '@/components/ui/input';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { Switch } from '@/components/ui/switch';
import { cn } from '@/lib/utils';

/** Sentinel for "no value" in Radix selects (which cannot use an empty value). */
export const NONE = '__none__';

interface BaseFieldProps {
  label: ReactNode;
  description?: ReactNode;
  error?: ReactNode;
  disabled?: boolean;
  className?: string;
}

export interface TextFieldProps extends BaseFieldProps {
  value: string;
  onChange: (value: string) => void;
  placeholder?: string;
  mono?: boolean;
  required?: boolean;
  inputMode?: 'numeric' | 'decimal' | 'text';
  maxLength?: number;
}

/** Labeled text input. */
export function TextField({
  label,
  description,
  error,
  disabled,
  className,
  value,
  onChange,
  placeholder,
  mono,
  required,
  inputMode,
  maxLength,
}: TextFieldProps) {
  return (
    <FormField
      label={label}
      description={description}
      error={error}
      required={required}
      className={className}
    >
      <Input
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        disabled={disabled}
        inputMode={inputMode}
        maxLength={maxLength}
        spellCheck={false}
        autoComplete="off"
        className={cn(mono && 'font-mono')}
      />
    </FormField>
  );
}

const NUMBER_INPUT = /^-?\d*\.?\d*$/;

/** Labeled numeric input keeping the raw text (empty = server default). */
export function NumberField(props: TextFieldProps & { integer?: boolean }) {
  const { t } = useTranslation('policies');
  const { integer, onChange, ...rest } = props;
  const trimmed = props.value.trim();
  const invalid =
    trimmed !== '' && (!NUMBER_INPUT.test(trimmed) || (integer === true && trimmed.includes('.')));
  return (
    <TextField
      {...rest}
      mono
      inputMode={integer ? 'numeric' : 'decimal'}
      onChange={onChange}
      error={
        props.error ?? (invalid ? t(integer ? 'builder.invalidInteger' : 'builder.invalidNumber') : undefined)
      }
    />
  );
}

export interface DurationFieldProps extends BaseFieldProps {
  value: string;
  onChange: (value: string) => void;
  placeholder?: string;
  allowPermanent?: boolean;
  allowEmpty?: boolean;
  required?: boolean;
}

/** Labeled duration input ("30s", "10m", "permanent"). */
export function DurationField({
  label,
  description,
  error,
  disabled,
  className,
  value,
  onChange,
  placeholder,
  allowPermanent,
  allowEmpty = true,
  required,
}: DurationFieldProps) {
  return (
    <FormField
      label={label}
      description={description}
      error={error}
      required={required}
      className={className}
    >
      <DurationInput
        value={value}
        onChange={onChange}
        placeholder={placeholder}
        allowPermanent={allowPermanent}
        allowEmpty={allowEmpty}
        disabled={disabled}
      />
    </FormField>
  );
}

export interface SelectOption {
  value: string;
  label: ReactNode;
}

export interface SelectFieldProps extends BaseFieldProps {
  value: string;
  onChange: (value: string) => void;
  options: readonly SelectOption[];
  placeholder?: string;
  required?: boolean;
  /** Adds a "none" option mapped to the empty string. */
  noneLabel?: ReactNode;
}

/** Labeled select; an empty value selects the "none" option when `noneLabel` is set. */
export function SelectField({
  label,
  description,
  error,
  disabled,
  className,
  value,
  onChange,
  options,
  placeholder,
  required,
  noneLabel,
}: SelectFieldProps) {
  const known = value === '' || options.some((o) => o.value === value);
  return (
    <FormField
      label={label}
      description={description}
      error={error}
      required={required}
      className={className}
    >
      <Select
        value={value === '' ? (noneLabel !== undefined ? NONE : '') : value}
        onValueChange={(v) => onChange(v === NONE ? '' : v)}
        disabled={disabled}
      >
        <SelectTrigger className="w-full">
          <SelectValue placeholder={placeholder} />
        </SelectTrigger>
        <SelectContent>
          {noneLabel !== undefined && <SelectItem value={NONE}>{noneLabel}</SelectItem>}
          {options.map((o) => (
            <SelectItem key={o.value} value={o.value}>
              {o.label}
            </SelectItem>
          ))}
          {!known && <SelectItem value={value}>{value}</SelectItem>}
        </SelectContent>
      </Select>
    </FormField>
  );
}

export interface SwitchFieldProps extends BaseFieldProps {
  checked: boolean;
  onChange: (checked: boolean) => void;
}

/** Switch with an inline label. */
export function SwitchField({
  label,
  description,
  disabled,
  className,
  checked,
  onChange,
}: SwitchFieldProps) {
  return (
    <FormField label={label} description={description} inline className={cn('min-h-9', className)}>
      <Switch checked={checked} onCheckedChange={onChange} disabled={disabled} />
    </FormField>
  );
}

export interface TagsFieldProps extends BaseFieldProps {
  value: readonly string[];
  onChange: (value: string[]) => void;
  placeholder?: string;
  validate?: (tag: string) => boolean;
  maxTags?: number;
}

/** Labeled chip input for string lists. */
export function TagsField({
  label,
  description,
  error,
  disabled,
  className,
  value,
  onChange,
  placeholder,
  validate,
  maxTags,
}: TagsFieldProps) {
  return (
    <FormField label={label} description={description} error={error} className={className}>
      <TagsInput
        value={value}
        onChange={onChange}
        placeholder={placeholder}
        validate={validate}
        maxTags={maxTags}
        disabled={disabled}
      />
    </FormField>
  );
}

export interface CheckboxGroupProps {
  label: ReactNode;
  value: readonly string[];
  onChange: (value: string[]) => void;
  options: readonly SelectOption[];
  disabled?: boolean;
  error?: ReactNode;
  className?: string;
}

/** Multi-select as a wrapping group of checkboxes (keeps the option order). */
export function CheckboxGroup({
  label,
  value,
  onChange,
  options,
  disabled,
  error,
  className,
}: CheckboxGroupProps) {
  const toggle = (option: string, checked: boolean) => {
    const next = checked ? [...value, option] : value.filter((v) => v !== option);
    const order = options.map((o) => o.value);
    onChange([...new Set(next)].sort((a, b) => order.indexOf(a) - order.indexOf(b)));
  };
  return (
    <fieldset className={cn('grid gap-1.5', className)} disabled={disabled}>
      <legend className="mb-1.5 text-sm leading-none font-medium">{label}</legend>
      <div className="flex flex-wrap gap-x-4 gap-y-2">
        {options.map((o) => (
          <label key={o.value} className="inline-flex items-center gap-2 text-sm">
            <Checkbox
              checked={value.includes(o.value)}
              onCheckedChange={(checked) => toggle(o.value, checked === true)}
              disabled={disabled}
            />
            {o.label}
          </label>
        ))}
      </div>
      {error && (
        <p role="alert" className="text-xs text-destructive">
          {error}
        </p>
      )}
    </fieldset>
  );
}

/** Titled group of fields inside a builder form. */
export function FieldSection({
  title,
  description,
  children,
  className,
}: {
  title: ReactNode;
  description?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <section className={cn('rounded-lg border bg-card p-4', className)}>
      <div className="mb-3 space-y-0.5">
        <h3 className="text-sm font-semibold">{title}</h3>
        {description && <p className="text-xs text-muted-foreground">{description}</p>}
      </div>
      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">{children}</div>
    </section>
  );
}
