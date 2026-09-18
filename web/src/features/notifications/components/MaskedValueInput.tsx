import { PencilIcon, Undo2Icon } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { SimpleTooltip } from '@/components/ui/tooltip';

import { isMaskedValue } from '../channelConfig';

export interface MaskedValueInputProps {
  value: string;
  onChange: (value: string) => void;
  /** Value returned by the server (possibly masked). */
  initial: string;
  /** Password input for credentials; URLs stay visible while typing. */
  secret?: boolean;
  placeholder?: string;
  id?: string;
  'aria-invalid'?: boolean;
  'aria-describedby'?: string;
}

/**
 * Input for settings the server masks on read. An untouched masked value is
 * read-only and sent back as is (the server keeps the stored value); "Replace"
 * clears it for a new value, "Keep current" restores the masked value.
 */
export function MaskedValueInput({
  value,
  onChange,
  initial,
  secret = false,
  placeholder,
  ...field
}: MaskedValueInputProps) {
  const { t } = useTranslation('notifications');
  const initialMasked = isMaskedValue(initial);
  const untouched = initialMasked && value === initial;

  if (untouched) {
    return (
      <div className="flex gap-2">
        <Input {...field} value={value} readOnly className="font-mono text-muted-foreground" />
        <SimpleTooltip content={t('masked.replaceHint')}>
          <Button type="button" variant="outline" onClick={() => onChange('')}>
            <PencilIcon />
            {t('masked.replace')}
          </Button>
        </SimpleTooltip>
      </div>
    );
  }
  return (
    <div className="flex gap-2">
      <Input
        {...field}
        value={value}
        type={secret ? 'password' : 'text'}
        autoComplete={secret ? 'new-password' : 'off'}
        spellCheck={false}
        placeholder={placeholder}
        className="font-mono"
        onChange={(e) => onChange(e.target.value)}
      />
      {initialMasked && (
        <SimpleTooltip content={t('masked.keepHint')}>
          <Button type="button" variant="ghost" onClick={() => onChange(initial)}>
            <Undo2Icon />
            {t('masked.keep')}
          </Button>
        </SimpleTooltip>
      )}
    </div>
  );
}
