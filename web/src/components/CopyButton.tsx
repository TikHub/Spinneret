import { CheckIcon, CopyIcon } from 'lucide-react';
import { useEffect, useState, type MouseEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import { Button, type ButtonProps } from '@/components/ui/button';
import { SimpleTooltip } from '@/components/ui/tooltip';
import { copyText } from '@/lib/clipboard';
import { cn } from '@/lib/utils';

const COPIED_RESET_MS = 1500;

export interface CopyButtonProps extends Omit<ButtonProps, 'onClick' | 'value'> {
  /** Text copied to the clipboard. */
  value: string;
  /** Accessible label / tooltip; defaults to "Copy". */
  label?: string;
}

/** Icon button copying a value to the clipboard with feedback. */
export function CopyButton({
  value,
  label,
  className,
  size = 'icon-sm',
  variant = 'ghost',
  ...props
}: CopyButtonProps) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    if (!copied) return undefined;
    const timer = setTimeout(() => setCopied(false), COPIED_RESET_MS);
    return () => clearTimeout(timer);
  }, [copied]);

  const copy = async (event: MouseEvent<HTMLButtonElement>) => {
    event.stopPropagation();
    try {
      await copyText(value, event.currentTarget.parentElement);
      setCopied(true);
    } catch {
      toast.error(t('actions.copyFailed'));
    }
  };

  const text = copied ? t('actions.copied') : (label ?? t('actions.copy'));
  return (
    <SimpleTooltip content={text}>
      <Button
        variant={variant}
        size={size}
        aria-label={text}
        className={cn('size-6 text-muted-foreground', className)}
        onClick={copy}
        {...props}
      >
        {copied ? <CheckIcon className="size-3.5 text-success" /> : <CopyIcon className="size-3.5" />}
      </Button>
    </SimpleTooltip>
  );
}

export interface IdTextProps {
  value: string;
  /** Characters kept when truncating (0 = wrap the whole value instead). */
  truncate?: number;
  className?: string;
  copy?: boolean;
}

/**
 * Monospaced identifier with a copy button (tables, detail panels).
 *
 * Untruncated values wrap on any character: a 40-character ID is a single
 * unbreakable "word", so without `wrap-anywhere` the text keeps its intrinsic
 * width and paints over whatever sits in the next column. `max-w-full` keeps
 * the row inside its grid cell and `shrink-0` keeps the copy button from being
 * squeezed, while `-my-1` keeps its 24px hit area from growing the line box.
 */
export function IdText({ value, truncate = 0, className, copy = true }: IdTextProps) {
  if (!value) return <span className="text-muted-foreground">—</span>;
  const shown = truncate > 0 && value.length > truncate ? `${value.slice(0, truncate)}…` : value;
  return (
    <span
      className={cn('inline-flex max-w-full items-start gap-0.5 font-mono text-xs', className)}
      title={value}
    >
      <span className={truncate > 0 ? 'truncate' : 'min-w-0 wrap-anywhere'}>{shown}</span>
      {copy && <CopyButton value={value} className="-my-1 shrink-0" />}
    </span>
  );
}
