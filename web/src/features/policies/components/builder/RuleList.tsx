import {
  ArrowDownIcon,
  ArrowUpIcon,
  ChevronRightIcon,
  CopyPlusIcon,
  PlusIcon,
  Trash2Icon,
} from 'lucide-react';
import { useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';

import { Button } from '@/components/ui/button';
import { cn } from '@/lib/utils';

export interface RuleListProps<T extends { key: string }> {
  title: ReactNode;
  description?: ReactNode;
  items: readonly T[];
  onChange: (items: T[]) => void;
  createItem: () => T;
  duplicateItem: (item: T) => T;
  renderSummary: (item: T, index: number) => ReactNode;
  renderEditor: (item: T, index: number, update: (item: T) => void) => ReactNode;
  addLabel: ReactNode;
  emptyLabel: ReactNode;
  /** Show the 1-based position (ordered rules). */
  numbered?: boolean;
  disabled?: boolean;
}

/** Ordered, collapsible list of rules with reorder, duplicate and delete controls. */
export function RuleList<T extends { key: string }>({
  title,
  description,
  items,
  onChange,
  createItem,
  duplicateItem,
  renderSummary,
  renderEditor,
  addLabel,
  emptyLabel,
  numbered = true,
  disabled,
}: RuleListProps<T>) {
  const { t } = useTranslation('policies');
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(() => new Set());

  const toggle = (key: string) =>
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  const move = (index: number, delta: number) => {
    const target = index + delta;
    if (target < 0 || target >= items.length) return;
    const next = [...items];
    const [moved] = next.splice(index, 1);
    if (moved) next.splice(target, 0, moved);
    onChange(next);
  };
  const add = () => {
    const item = createItem();
    onChange([...items, item]);
    setExpanded((prev) => new Set(prev).add(item.key));
  };
  const duplicate = (index: number) => {
    const source = items[index];
    if (!source) return;
    const copy = duplicateItem(source);
    const next = [...items];
    next.splice(index + 1, 0, copy);
    onChange(next);
    setExpanded((prev) => new Set(prev).add(copy.key));
  };

  return (
    <section className="rounded-lg border bg-card">
      <div className="flex flex-wrap items-center justify-between gap-2 border-b px-4 py-3">
        <div className="space-y-0.5">
          <h3 className="text-sm font-semibold">
            {title} <span className="text-muted-foreground tabular">({items.length})</span>
          </h3>
          {description && <p className="text-xs text-muted-foreground">{description}</p>}
        </div>
        <Button variant="outline" size="sm" onClick={add} disabled={disabled}>
          <PlusIcon />
          {addLabel}
        </Button>
      </div>
      {items.length === 0 ? (
        <p className="px-4 py-6 text-center text-sm text-muted-foreground">{emptyLabel}</p>
      ) : (
        <ol className="divide-y">
          {items.map((item, index) => {
            const open = expanded.has(item.key);
            const editorId = `rule-editor-${item.key}`;
            return (
              <li key={item.key} className={cn(open && 'bg-muted/20')}>
                <div className="flex items-center gap-1 px-2 py-1.5">
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    aria-expanded={open}
                    aria-controls={editorId}
                    aria-label={
                      open
                        ? t('rules.collapse', { index: index + 1 })
                        : t('rules.expand', { index: index + 1 })
                    }
                    onClick={() => toggle(item.key)}
                  >
                    <ChevronRightIcon className={cn('transition-transform', open && 'rotate-90')} />
                  </Button>
                  <button
                    type="button"
                    className="flex min-w-0 flex-1 items-center gap-2 text-left"
                    onClick={() => toggle(item.key)}
                  >
                    {numbered && (
                      <span className="w-6 shrink-0 text-right font-mono text-xs text-muted-foreground tabular">
                        {index + 1}.
                      </span>
                    )}
                    <span className="min-w-0 flex-1">{renderSummary(item, index)}</span>
                  </button>
                  <div className="flex shrink-0 items-center">
                    {numbered && (
                      <>
                        <Button
                          variant="ghost"
                          size="icon-sm"
                          disabled={disabled || index === 0}
                          aria-label={t('rules.moveUp', { index: index + 1 })}
                          onClick={() => move(index, -1)}
                        >
                          <ArrowUpIcon />
                        </Button>
                        <Button
                          variant="ghost"
                          size="icon-sm"
                          disabled={disabled || index === items.length - 1}
                          aria-label={t('rules.moveDown', { index: index + 1 })}
                          onClick={() => move(index, 1)}
                        >
                          <ArrowDownIcon />
                        </Button>
                      </>
                    )}
                    <Button
                      variant="ghost"
                      size="icon-sm"
                      disabled={disabled}
                      aria-label={t('rules.duplicate', { index: index + 1 })}
                      onClick={() => duplicate(index)}
                    >
                      <CopyPlusIcon />
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon-sm"
                      disabled={disabled}
                      aria-label={t('rules.remove', { index: index + 1 })}
                      onClick={() => onChange(items.filter((x) => x.key !== item.key))}
                    >
                      <Trash2Icon />
                    </Button>
                  </div>
                </div>
                {open && (
                  <div id={editorId} className="border-t px-4 py-4">
                    {renderEditor(item, index, (updated) =>
                      onChange(items.map((x) => (x.key === item.key ? updated : x))),
                    )}
                  </div>
                )}
              </li>
            );
          })}
        </ol>
      )}
    </section>
  );
}
