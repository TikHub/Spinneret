import { EyeIcon, EyeOffIcon } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';

import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { cn } from '@/lib/utils';

import { passwordStrength } from '../../userForm';

const STRENGTH_CLASSES = [
  'bg-destructive',
  'bg-destructive',
  'bg-amber-500',
  'bg-emerald-500',
  'bg-emerald-600',
];

export interface PasswordInputProps {
  value: string;
  onChange: (value: string) => void;
  /** Show the strength meter under the input. */
  showStrength?: boolean;
  autoComplete?: string;
  id?: string;
  'aria-invalid'?: boolean;
  'aria-describedby'?: string;
}

/** Password input with a visibility toggle and an optional strength hint. */
export function PasswordInput({
  value,
  onChange,
  showStrength = false,
  autoComplete = 'new-password',
  id,
  ...aria
}: PasswordInputProps) {
  const { t } = useTranslation('access');
  const [visible, setVisible] = useState(false);
  const strength = passwordStrength(value);
  return (
    <div className="grid gap-1.5">
      <div className="relative">
        <Input
          id={id}
          type={visible ? 'text' : 'password'}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          autoComplete={autoComplete}
          spellCheck={false}
          className="pr-10"
          {...aria}
        />
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          className="absolute top-1/2 right-1 size-7 -translate-y-1/2 text-muted-foreground"
          aria-label={visible ? t('password.hide') : t('password.show')}
          aria-pressed={visible}
          onClick={() => setVisible((v) => !v)}
        >
          {visible ? <EyeOffIcon /> : <EyeIcon />}
        </Button>
      </div>
      {showStrength && value !== '' && (
        <div className="flex items-center gap-2" aria-live="polite">
          <div className="grid flex-1 grid-cols-4 gap-1" aria-hidden>
            {[1, 2, 3, 4].map((step) => (
              <span
                key={step}
                className={cn('h-1 rounded-full', strength >= step ? STRENGTH_CLASSES[strength] : 'bg-muted')}
              />
            ))}
          </div>
          <span className="text-xs text-muted-foreground">{t(`password.strength.${strength}`)}</span>
        </div>
      )}
    </div>
  );
}
