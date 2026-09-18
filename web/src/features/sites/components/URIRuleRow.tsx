import { ArrowDownIcon, ArrowUpIcon, GripVerticalIcon, Trash2Icon } from 'lucide-react';
import { type DragEvent, type KeyboardEvent } from 'react';
import { useTranslation } from 'react-i18next';

import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { cn } from '@/lib/utils';

import { URI_RULE_KINDS, type RuleDraft, type RuleHint, type URIRuleKind } from '../uriRules';

export interface URIRuleRowProps {
  rule: RuleDraft;
  index: number;
  count: number;
  hints: readonly RuleHint[];
  /** Error returned by the server for this row. */
  serverError?: string;
  readOnly: boolean;
  dragging: boolean;
  dropTarget: boolean;
  /** The drag handle is pressed; the row becomes draggable. */
  armed: boolean;
  onArm: (armed: boolean) => void;
  onChange: (patch: Partial<RuleDraft>) => void;
  onMove: (to: number) => void;
  onRemove: () => void;
  onDragStart: (event: DragEvent<HTMLLIElement>) => void;
  onDragOver: (event: DragEvent<HTMLLIElement>) => void;
  onDrop: (event: DragEvent<HTMLLIElement>) => void;
  onDragEnd: () => void;
}

const HINT_CLASS: Record<RuleHint['level'], string> = {
  error: 'text-destructive',
  warning: 'text-amber-700 dark:text-amber-400',
  info: 'text-muted-foreground',
};

/** One editable URI rule: drag handle, kind, pattern, move and remove buttons, hints. */
export function URIRuleRow({
  rule,
  index,
  count,
  hints,
  serverError,
  readOnly,
  dragging,
  dropTarget,
  armed,
  onArm,
  onChange,
  onMove,
  onRemove,
  onDragStart,
  onDragOver,
  onDrop,
  onDragEnd,
}: URIRuleRowProps) {
  const { t } = useTranslation('sites');
  const position = index + 1;
  const hintId = `${rule.key}-hints`;
  const invalid = Boolean(serverError) || hints.some((h) => h.level === 'error');

  const onHandleKeyDown = (event: KeyboardEvent<HTMLButtonElement>) => {
    if (event.key === 'ArrowUp' && index > 0) {
      event.preventDefault();
      onMove(index - 1);
    } else if (event.key === 'ArrowDown' && index < count - 1) {
      event.preventDefault();
      onMove(index + 1);
    }
  };

  return (
    <li
      draggable={!readOnly && armed}
      onDragStart={onDragStart}
      onDragOver={onDragOver}
      onDrop={onDrop}
      onDragEnd={onDragEnd}
      data-rule-key={rule.key}
      className={cn(
        'grid gap-1 rounded-md border bg-card p-2 transition-colors',
        dragging && 'opacity-50',
        dropTarget && 'border-primary ring-1 ring-primary',
        invalid && 'border-destructive/50',
      )}
    >
      <div className="flex items-center gap-1.5">
        <button
          type="button"
          data-rule-handle={rule.key}
          disabled={readOnly}
          className="flex h-8 w-5 cursor-grab items-center justify-center rounded text-muted-foreground hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none active:cursor-grabbing disabled:cursor-default disabled:opacity-40"
          aria-label={t('rules.dragHandle', { position })}
          onPointerDown={() => onArm(true)}
          onPointerUp={() => onArm(false)}
          onKeyDown={onHandleKeyDown}
        >
          <GripVerticalIcon className="size-4" />
        </button>
        <span className="tabular w-6 text-right text-xs text-muted-foreground">{position}</span>
        <Select
          value={rule.kind}
          onValueChange={(kind) => onChange({ kind: kind as URIRuleKind })}
          disabled={readOnly}
        >
          <SelectTrigger size="sm" className="w-28 shrink-0" aria-label={t('rules.kindLabel', { position })}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {URI_RULE_KINDS.map((kind) => (
              <SelectItem key={kind} value={kind}>
                {t(`rules.kinds.${kind}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Input
          value={rule.pattern}
          onChange={(e) => onChange({ pattern: e.target.value })}
          readOnly={readOnly}
          className="h-8 min-w-0 flex-1 font-mono text-xs"
          spellCheck={false}
          autoComplete="off"
          placeholder={t(`rules.placeholders.${rule.kind}`)}
          aria-label={t('rules.patternLabel', { position })}
          aria-invalid={invalid || undefined}
          aria-describedby={hints.length > 0 || serverError ? hintId : undefined}
        />
        <Button
          variant="ghost"
          size="icon-sm"
          className="size-8"
          disabled={readOnly || index === 0}
          onClick={() => onMove(index - 1)}
          aria-label={t('rules.moveUp', { position })}
        >
          <ArrowUpIcon />
        </Button>
        <Button
          variant="ghost"
          size="icon-sm"
          className="size-8"
          disabled={readOnly || index === count - 1}
          onClick={() => onMove(index + 1)}
          aria-label={t('rules.moveDown', { position })}
        >
          <ArrowDownIcon />
        </Button>
        <Button
          variant="ghost"
          size="icon-sm"
          className="size-8 text-muted-foreground hover:text-destructive"
          disabled={readOnly}
          onClick={onRemove}
          aria-label={t('rules.remove', { position })}
        >
          <Trash2Icon />
        </Button>
      </div>
      {(hints.length > 0 || serverError) && (
        <ul id={hintId} className="grid gap-0.5 pl-14 text-xs">
          {serverError && (
            <li role="alert" className="text-destructive">
              {serverError}
            </li>
          )}
          {hints.map((hint, hintIndex) => (
            // A code can repeat within one pattern (e.g. two malformed template segments).
            <li key={`${hint.code}-${hintIndex}`} className={HINT_CLASS[hint.level]}>
              {t(`rules.hints.${hint.code}`, hint.params)}
            </li>
          ))}
        </ul>
      )}
    </li>
  );
}
