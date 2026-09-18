import { ChevronDownIcon } from 'lucide-react';
import { useId } from 'react';
import { useTranslation } from 'react-i18next';

import { Button } from '@/components/ui/button';
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import { Input } from '@/components/ui/input';
import {
  DURATION_PRESETS,
  humanizeDuration,
  parseDuration,
  PERMANENT,
  validateDurationInput,
} from '@/lib/duration';
import { cn } from '@/lib/utils';

export interface DurationInputProps {
  /** Duration string ("30m", "7d", "permanent"). */
  value: string;
  onChange: (value: string) => void;
  /** Offer and accept "permanent". */
  allowPermanent?: boolean;
  /** Accept an empty value (e.g. "use policy default"). */
  allowEmpty?: boolean;
  presets?: readonly string[];
  placeholder?: string;
  disabled?: boolean;
  id?: string;
  className?: string;
  'aria-invalid'?: boolean;
  'aria-describedby'?: string;
}

/**
 * Text input for backend duration strings with presets, inline validation and
 * a humanized preview. Validation state is exposed through aria-invalid; use
 * validateDurationInput() to block submission.
 */
export function DurationInput({
  value,
  onChange,
  allowPermanent = false,
  allowEmpty = false,
  presets = DURATION_PRESETS,
  placeholder,
  disabled,
  id,
  className,
  ...aria
}: DurationInputProps) {
  const { t } = useTranslation();
  const generatedId = useId();
  const inputId = id ?? generatedId;
  const status = validateDurationInput(value, { allowPermanent, allowEmpty });
  const parsed = parseDuration(value);
  const invalid = value.trim() !== '' && status !== 'ok';
  const hintId = `${inputId}-hint`;

  let hint = '';
  if (status === 'invalid') hint = t('duration.invalid');
  else if (status === 'permanent') hint = t('duration.permanentNotAllowed');
  else if (parsed?.permanent) hint = t('duration.permanent');
  else if (parsed && parsed.ms > 0) hint = humanizeDuration(parsed.ms, { maxUnits: 3, t });

  return (
    <div className={cn('grid gap-1', className)}>
      <div className="flex gap-1">
        <Input
          id={inputId}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          placeholder={placeholder ?? t('duration.placeholder')}
          disabled={disabled}
          spellCheck={false}
          autoComplete="off"
          className="font-mono"
          aria-invalid={invalid || aria['aria-invalid']}
          aria-describedby={
            [aria['aria-describedby'], hint ? hintId : undefined].filter(Boolean).join(' ') || undefined
          }
        />
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="outline" size="icon" disabled={disabled} aria-label={t('duration.presets')}>
              <ChevronDownIcon />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuLabel>{t('duration.presets')}</DropdownMenuLabel>
            {presets.map((preset) => (
              <DropdownMenuItem key={preset} onSelect={() => onChange(preset)} className="font-mono">
                {preset}
              </DropdownMenuItem>
            ))}
            {allowPermanent && (
              <>
                <DropdownMenuSeparator />
                <DropdownMenuItem onSelect={() => onChange(PERMANENT)}>
                  {t('duration.permanent')}
                </DropdownMenuItem>
              </>
            )}
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
      {hint && (
        <p id={hintId} className={cn('text-xs', invalid ? 'text-destructive' : 'text-muted-foreground')}>
          {hint}
        </p>
      )}
    </div>
  );
}
