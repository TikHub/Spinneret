import { EyeIcon, EyeOffIcon } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';

import { Button } from '@/components/ui/button';
import { Textarea } from '@/components/ui/textarea';
import { cn } from '@/lib/utils';

export interface SecretValueInputProps {
  value: string;
  onChange: (value: string) => void;
  placeholder?: string;
  disabled?: boolean;
  id?: string;
  'aria-invalid'?: boolean;
  'aria-describedby'?: string;
}

/**
 * Multi-line secret value input, masked by default (password-style glyphs)
 * with a show/hide toggle. Autocomplete, spellcheck and autocorrect are off so
 * the value is not stored or sent to spelling services.
 */
export function SecretValueInput({
  value,
  onChange,
  placeholder,
  disabled,
  ...field
}: SecretValueInputProps) {
  const { t } = useTranslation('secrets');
  const [visible, setVisible] = useState(false);
  return (
    <div className="relative">
      <Textarea
        {...field}
        value={value}
        disabled={disabled}
        placeholder={placeholder}
        rows={4}
        autoComplete="off"
        autoCorrect="off"
        autoCapitalize="off"
        spellCheck={false}
        data-1p-ignore
        className={cn('pr-10 font-mono text-xs', !visible && '[-webkit-text-security:disc]')}
        onChange={(e) => onChange(e.target.value)}
      />
      <Button
        type="button"
        variant="ghost"
        size="icon-sm"
        className="absolute top-1 right-1 size-7"
        aria-label={visible ? t('form.hideValue') : t('form.showValue')}
        aria-pressed={visible}
        disabled={disabled}
        onClick={() => setVisible((v) => !v)}
      >
        {visible ? <EyeOffIcon /> : <EyeIcon />}
      </Button>
    </div>
  );
}
