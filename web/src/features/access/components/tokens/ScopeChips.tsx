import { useTranslation } from 'react-i18next';

import { SimpleTooltip } from '@/components/ui/tooltip';
import { cn } from '@/lib/utils';

import { toKeySegment } from '../../forms';
import { parseScope, type ParsedScope } from '../../scopes';

function ScopeChip({ scope }: { scope: ParsedScope }) {
  const { t } = useTranslation('access');
  const broad = scope.name === 'admin';
  const title = scope.known
    ? t(`scopes.names.${toKeySegment(scope.name)}.grants`, { defaultValue: scope.raw })
    : t('scopes.unknown');
  return (
    <SimpleTooltip content={title}>
      <span
        tabIndex={0}
        className={cn(
          'inline-flex max-w-56 items-center rounded border px-1.5 py-0.5 font-mono text-[11px] leading-4 outline-none focus-visible:ring-2 focus-visible:ring-ring',
          broad
            ? 'border-amber-500/30 bg-amber-500/10 text-amber-800 dark:text-amber-300'
            : scope.known
              ? 'bg-muted/40'
              : 'border-destructive/30 bg-destructive/5 text-destructive',
        )}
      >
        <span className="shrink-0">{scope.name}</span>
        {scope.arg && <span className="truncate text-muted-foreground">:{scope.arg}</span>}
      </span>
    </SimpleTooltip>
  );
}

export interface ScopeChipsProps {
  scopes: readonly string[];
  /** Chips shown before collapsing the rest into "+N". */
  max?: number;
  className?: string;
}

/** Compact scope list: name and argument per chip, the rest summarized in a tooltip. */
export function ScopeChips({ scopes, max = 3, className }: ScopeChipsProps) {
  const { t } = useTranslation('access');
  if (scopes.length === 0) return <span className="text-muted-foreground">—</span>;
  const parsed = scopes.map(parseScope);
  const visible = parsed.slice(0, max);
  const hidden = parsed.slice(max);
  return (
    <span className={cn('flex flex-wrap items-center gap-1', className)}>
      {visible.map((scope) => (
        <ScopeChip key={scope.raw} scope={scope} />
      ))}
      {hidden.length > 0 && (
        <SimpleTooltip
          content={
            <span className="grid gap-0.5 font-mono">
              {hidden.map((scope) => (
                <span key={scope.raw}>{scope.raw}</span>
              ))}
            </span>
          }
        >
          <span
            tabIndex={0}
            className="rounded px-1 text-xs text-muted-foreground outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            {t('scopes.more', { count: hidden.length })}
          </span>
        </SimpleTooltip>
      )}
    </span>
  );
}
