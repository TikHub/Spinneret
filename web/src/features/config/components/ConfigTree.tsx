import { ChevronDownIcon, ChevronRightIcon, FileCode2Icon, FolderIcon, LockIcon } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';

import { EmptyState } from '@/components/EmptyState';
import { ErrorState } from '@/components/ErrorState';
import { Skeleton } from '@/components/ui/skeleton';
import { SimpleTooltip } from '@/components/ui/tooltip';
import { cn } from '@/lib/utils';

import { type ConfigTreeGroup } from '../configModel';
import { type ConfigSelection } from '../useConfigApi';

export interface ConfigTreeProps {
  groups: readonly ConfigTreeGroup[];
  selected: ConfigSelection | undefined;
  onSelect: (selection: ConfigSelection) => void;
  isLoading: boolean;
  error: unknown;
  onRetry: () => void;
  filtered: boolean;
}

function GroupNode({
  group,
  selected,
  onSelect,
}: {
  group: ConfigTreeGroup;
  selected: ConfigSelection | undefined;
  onSelect: (selection: ConfigSelection) => void;
}) {
  const { t } = useTranslation('config');
  const [open, setOpen] = useState(true);
  const Chevron = open ? ChevronDownIcon : ChevronRightIcon;
  return (
    <li>
      <button
        type="button"
        className="flex w-full items-center gap-1.5 rounded-md px-2 py-1 text-left text-sm font-medium hover:bg-muted"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
      >
        <Chevron className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
        <FolderIcon className="size-4 shrink-0 text-muted-foreground" aria-hidden />
        <span className="truncate font-mono text-xs">{group.group}</span>
        {group.system && (
          <SimpleTooltip content={t('tree.systemGroup')}>
            <LockIcon
              className="size-3.5 shrink-0 text-amber-600 dark:text-amber-400"
              aria-label={t('tree.systemGroup')}
            />
          </SimpleTooltip>
        )}
        <span className="ml-auto text-xs text-muted-foreground tabular">{group.items.length}</span>
      </button>
      {open && (
        <ul className="ml-4 border-l pl-1.5">
          {group.items.map((item) => {
            const active = selected?.group === item.group && selected.key === item.key;
            return (
              <li key={item.key}>
                <button
                  type="button"
                  aria-current={active ? 'true' : undefined}
                  className={cn(
                    'flex w-full items-center gap-1.5 rounded-md px-2 py-1 text-left hover:bg-muted',
                    active && 'bg-primary/10 text-primary hover:bg-primary/15',
                  )}
                  onClick={() => onSelect({ group: item.group, key: item.key })}
                >
                  <FileCode2Icon className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
                  <span className="truncate font-mono text-xs" title={item.key}>
                    {item.key}
                  </span>
                  <span className="ml-auto flex shrink-0 items-center gap-1">
                    {item.hasDraft && (
                      <SimpleTooltip content={t('tree.hasDraft')}>
                        <span
                          className="size-1.5 rounded-full bg-amber-500"
                          aria-label={t('tree.hasDraft')}
                        />
                      </SimpleTooltip>
                    )}
                    <span className="text-[10px] text-muted-foreground tabular">
                      {item.currentVersion > 0 ? `v${item.currentVersion}` : t('tree.unpublished')}
                    </span>
                  </span>
                </button>
              </li>
            );
          })}
        </ul>
      )}
    </li>
  );
}

/** Group → key tree of the config items on the current page. */
export function ConfigTree({
  groups,
  selected,
  onSelect,
  isLoading,
  error,
  onRetry,
  filtered,
}: ConfigTreeProps) {
  const { t } = useTranslation('config');
  if (isLoading) {
    return (
      <div className="grid gap-2 p-2" role="status" aria-label={t('common:table.loading')}>
        {Array.from({ length: 8 }, (_, i) => (
          <Skeleton key={i} className={cn('h-5', i % 3 === 0 ? 'w-2/3' : 'ml-5 w-3/4')} />
        ))}
      </div>
    );
  }
  if (error && groups.length === 0) {
    return <ErrorState error={error} onRetry={onRetry} compact />;
  }
  if (groups.length === 0) {
    return (
      <EmptyState
        compact
        icon={FileCode2Icon}
        title={filtered ? t('tree.noMatches') : t('tree.empty')}
        description={filtered ? undefined : t('tree.emptyDescription')}
      />
    );
  }
  return (
    <nav aria-label={t('tree.label')}>
      <ul className="grid gap-0.5 p-1">
        {groups.map((group) => (
          <GroupNode key={group.group} group={group} selected={selected} onSelect={onSelect} />
        ))}
      </ul>
    </nav>
  );
}
