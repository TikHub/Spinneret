import { XIcon } from 'lucide-react';
import { useState, type ClipboardEvent, type KeyboardEvent } from 'react';
import { useTranslation } from 'react-i18next';

import { cn } from '@/lib/utils';

export interface TagsInputProps {
  value: readonly string[];
  onChange: (tags: string[]) => void;
  placeholder?: string;
  /** Maximum number of tags. */
  maxTags?: number;
  /** Returns true when a tag is acceptable. */
  validate?: (tag: string) => boolean;
  disabled?: boolean;
  id?: string;
  className?: string;
  'aria-invalid'?: boolean;
  'aria-describedby'?: string;
}

const SEPARATORS = /[,\n\t]/;

/** Chip input for string lists: Enter/comma adds, Backspace removes the last tag, paste splits on commas. */
export function TagsInput({
  value,
  onChange,
  placeholder,
  maxTags,
  validate,
  disabled,
  id,
  className,
  ...aria
}: TagsInputProps) {
  const { t } = useTranslation();
  const [draft, setDraft] = useState('');
  const [error, setError] = useState<string>();

  const addTags = (raw: string[]) => {
    const next = [...value];
    for (const candidate of raw.map((s) => s.trim()).filter(Boolean)) {
      if (next.includes(candidate)) continue;
      if (maxTags !== undefined && next.length >= maxTags) {
        setError(t('tags.limit', { max: maxTags }));
        break;
      }
      if (validate && !validate(candidate)) {
        setError(t('tags.invalid'));
        continue;
      }
      next.push(candidate);
    }
    if (next.length !== value.length) onChange(next);
  };

  const commitDraft = () => {
    if (!draft.trim()) return;
    setError(undefined);
    addTags([draft]);
    setDraft('');
  };

  const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === 'Enter' || event.key === ',') {
      event.preventDefault();
      commitDraft();
    } else if (event.key === 'Backspace' && draft === '' && value.length > 0) {
      onChange(value.slice(0, -1));
    }
  };

  const onPaste = (event: ClipboardEvent<HTMLInputElement>) => {
    const text = event.clipboardData.getData('text');
    if (!SEPARATORS.test(text)) return;
    event.preventDefault();
    setError(undefined);
    addTags(text.split(SEPARATORS));
  };

  return (
    <div className={cn('grid gap-1', className)}>
      <div
        className={cn(
          'flex min-h-9 w-full flex-wrap items-center gap-1 rounded-md border border-input bg-background px-2 py-1 shadow-xs focus-within:border-ring focus-within:ring-[3px] focus-within:ring-ring/50',
          disabled && 'cursor-not-allowed opacity-50',
          (aria['aria-invalid'] || error) && 'border-destructive',
        )}
      >
        {value.map((tag) => (
          <span
            key={tag}
            className="inline-flex items-center gap-1 rounded bg-secondary px-1.5 py-0.5 text-xs"
          >
            {tag}
            {!disabled && (
              <button
                type="button"
                className="rounded text-muted-foreground hover:text-foreground"
                aria-label={t('tags.remove', { tag })}
                onClick={() => onChange(value.filter((v) => v !== tag))}
              >
                <XIcon className="size-3" />
              </button>
            )}
          </span>
        ))}
        <input
          id={id}
          value={draft}
          disabled={disabled}
          onChange={(e) => {
            setDraft(e.target.value);
            setError(undefined);
          }}
          onKeyDown={onKeyDown}
          onPaste={onPaste}
          onBlur={commitDraft}
          placeholder={value.length === 0 ? (placeholder ?? t('tags.placeholder')) : undefined}
          className="min-w-24 flex-1 bg-transparent py-0.5 text-sm outline-none placeholder:text-muted-foreground"
          aria-invalid={aria['aria-invalid'] || Boolean(error)}
          aria-describedby={aria['aria-describedby']}
        />
      </div>
      {error && <p className="text-xs text-destructive">{error}</p>}
    </div>
  );
}
