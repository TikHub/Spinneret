import { FolderIcon, FolderOpenIcon, LayersIcon } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { ErrorState } from '@/components/ErrorState';
import { Skeleton } from '@/components/ui/skeleton';
import { cn } from '@/lib/utils';

import { type SecretFolder } from '../secretPath';

export interface SecretFolderTreeProps {
  folders: readonly SecretFolder[];
  total: number;
  selected: string;
  onSelect: (prefix: string) => void;
  isLoading: boolean;
  error: unknown;
  onRetry: () => void;
  truncated: boolean;
}

function FolderNode({
  folder,
  depth,
  selected,
  onSelect,
}: {
  folder: SecretFolder;
  depth: number;
  selected: string;
  onSelect: (prefix: string) => void;
}) {
  const active = selected === folder.prefix;
  const expanded = selected.startsWith(folder.prefix);
  const Icon = expanded ? FolderOpenIcon : FolderIcon;
  return (
    <li>
      <button
        type="button"
        aria-current={active ? 'true' : undefined}
        className={cn(
          'flex w-full items-center gap-1.5 rounded-md py-1 pr-2 text-left hover:bg-muted',
          active && 'bg-primary/10 text-primary hover:bg-primary/15',
        )}
        style={{ paddingLeft: `${0.5 + depth * 0.875}rem` }}
        onClick={() => onSelect(active ? '' : folder.prefix)}
      >
        <Icon className="size-4 shrink-0 text-muted-foreground" aria-hidden />
        <span className="truncate font-mono text-xs" title={folder.prefix}>
          {folder.name}
        </span>
        <span className="ml-auto text-xs text-muted-foreground tabular">{folder.count}</span>
      </button>
      {expanded && folder.children.length > 0 && (
        <ul>
          {folder.children.map((child) => (
            <FolderNode
              key={child.prefix}
              folder={child}
              depth={depth + 1}
              selected={selected}
              onSelect={onSelect}
            />
          ))}
        </ul>
      )}
    </li>
  );
}

/** Folder navigation derived from secret paths ("a/b/c" lives in folders "a/" and "a/b/"). */
export function SecretFolderTree({
  folders,
  total,
  selected,
  onSelect,
  isLoading,
  error,
  onRetry,
  truncated,
}: SecretFolderTreeProps) {
  const { t } = useTranslation('secrets');
  if (isLoading) {
    return (
      <div className="grid gap-2 p-2" role="status" aria-label={t('common:table.loading')}>
        {Array.from({ length: 6 }, (_, i) => (
          <Skeleton key={i} className="h-5 w-3/4" />
        ))}
      </div>
    );
  }
  // A failed background refresh keeps the loaded folders.
  if (error && total === 0) return <ErrorState error={error} onRetry={onRetry} compact />;
  return (
    <nav aria-label={t('folders.label')} className="p-1">
      <ul className="grid gap-0.5">
        <li>
          <button
            type="button"
            aria-current={selected === '' ? 'true' : undefined}
            className={cn(
              'flex w-full items-center gap-1.5 rounded-md px-2 py-1 text-left text-sm hover:bg-muted',
              selected === '' && 'bg-primary/10 text-primary hover:bg-primary/15',
            )}
            onClick={() => onSelect('')}
          >
            <LayersIcon className="size-4 shrink-0 text-muted-foreground" aria-hidden />
            <span>{t('folders.all')}</span>
            <span className="ml-auto text-xs text-muted-foreground tabular">
              {truncated ? `${total}+` : total}
            </span>
          </button>
        </li>
        {folders.map((folder) => (
          <FolderNode key={folder.prefix} folder={folder} depth={0} selected={selected} onSelect={onSelect} />
        ))}
      </ul>
      {folders.length === 0 && <p className="px-2 py-3 text-xs text-muted-foreground">{t('folders.none')}</p>}
      {truncated && <p className="px-2 py-2 text-xs text-muted-foreground">{t('folders.truncated')}</p>}
    </nav>
  );
}
