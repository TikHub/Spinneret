import { PlusIcon, Trash2Icon } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { duplicateKeys, type KeyValuePair } from '@/lib/keyValue';
import { cn } from '@/lib/utils';

export interface KeyValueEditorProps {
  value: readonly KeyValuePair[];
  onChange: (pairs: KeyValuePair[]) => void;
  keyPlaceholder?: string;
  valuePlaceholder?: string;
  disabled?: boolean;
  /** Mask values (e.g. headers containing credentials). */
  secretValues?: boolean;
  className?: string;
}

/** Ordered key/value list editor (labels, headers, template variables). */
export function KeyValueEditor({
  value,
  onChange,
  keyPlaceholder,
  valuePlaceholder,
  disabled,
  secretValues = false,
  className,
}: KeyValueEditorProps) {
  const { t } = useTranslation();
  const dupes = duplicateKeys(value);

  const update = (index: number, patch: Partial<KeyValuePair>) =>
    onChange(value.map((pair, i) => (i === index ? { ...pair, ...patch } : pair)));

  return (
    <div className={cn('grid gap-2', className)}>
      {value.length === 0 && <p className="text-xs text-muted-foreground">{t('keyValue.empty')}</p>}
      {value.map((pair, index) => {
        const duplicate = dupes.has(pair.key.trim());
        return (
          <div key={index} className="grid grid-cols-[1fr_1fr_auto] items-start gap-2">
            <div className="grid gap-0.5">
              <Input
                value={pair.key}
                disabled={disabled}
                placeholder={keyPlaceholder ?? t('keyValue.key')}
                aria-label={t('keyValue.key')}
                aria-invalid={duplicate}
                className="font-mono"
                onChange={(e) => update(index, { key: e.target.value })}
              />
              {duplicate && <span className="text-xs text-destructive">{t('keyValue.duplicate')}</span>}
            </div>
            <Input
              value={pair.value}
              disabled={disabled}
              type={secretValues ? 'password' : 'text'}
              placeholder={valuePlaceholder ?? t('keyValue.value')}
              aria-label={t('keyValue.value')}
              className="font-mono"
              onChange={(e) => update(index, { value: e.target.value })}
            />
            <Button
              variant="ghost"
              size="icon"
              disabled={disabled}
              aria-label={t('keyValue.remove')}
              onClick={() => onChange(value.filter((_, i) => i !== index))}
            >
              <Trash2Icon />
            </Button>
          </div>
        );
      })}
      <div>
        <Button
          variant="outline"
          size="sm"
          disabled={disabled}
          onClick={() => onChange([...value, { key: '', value: '' }])}
        >
          <PlusIcon />
          {t('keyValue.add')}
        </Button>
      </div>
    </div>
  );
}
