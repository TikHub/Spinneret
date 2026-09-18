import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';

import { DiffView } from '@/components/editor/DiffView';
import { ErrorState } from '@/components/ErrorState';
import { FormField } from '@/components/ui/form';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { type ConfigItemInfo, type ConfigVersion } from '@/gen/spinneret/v1/config_admin_pb';

import { formatLanguage } from '../configModel';
import { lineStats } from '../draftState';
import { useConfigDiff } from '../useConfigApi';

/** Right-hand side value meaning "the draft" (DiffConfigVersions.to_version = 0). */
export const DRAFT_SIDE = 0;

export interface CompareSides {
  from: number;
  to: number;
}

export interface VersionCompareProps {
  item: ConfigItemInfo;
  versions: readonly ConfigVersion[];
  sides: CompareSides | undefined;
  onSidesChange: (sides: CompareSides) => void;
}

/** Server-side diff between two versions, or a version and the draft. */
export function VersionCompare({ item, versions, sides, onSidesChange }: VersionCompareProps) {
  const { t } = useTranslation('config');
  // Versions of the loaded page plus the current version, newest first.
  const numbers = [...new Set([item.currentVersion, ...versions.map((v) => v.version)])]
    .filter((n) => n > 0)
    .sort((a, b) => b - a);
  const from = sides?.from ?? item.currentVersion;
  const to = sides?.to ?? (item.hasDraft ? DRAFT_SIDE : item.currentVersion);
  const ready = from > 0 && (to > 0 || (to === DRAFT_SIDE && item.hasDraft)) && from !== to;
  const diff = useConfigDiff(item.id, from, to, ready);
  const stats = useMemo(
    () => (diff.data ? lineStats(diff.data.fromContent, diff.data.toContent) : undefined),
    [diff.data],
  );

  const versionItems = (list: readonly number[]) =>
    list.map((n) => (
      <SelectItem key={n} value={String(n)}>
        {n === item.currentVersion ? t('versions.currentOption', { version: n }) : `v${n}`}
      </SelectItem>
    ));

  return (
    <div className="grid gap-3 rounded-lg border p-3">
      <div className="flex flex-wrap items-end gap-3">
        <FormField label={t('versions.compareFrom')}>
          <Select value={String(from)} onValueChange={(v) => onSidesChange({ from: Number(v), to })}>
            <SelectTrigger size="sm" className="w-40">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>{versionItems(numbers)}</SelectContent>
          </Select>
        </FormField>
        <FormField label={t('versions.compareTo')}>
          <Select value={String(to)} onValueChange={(v) => onSidesChange({ from, to: Number(v) })}>
            <SelectTrigger size="sm" className="w-40">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {item.hasDraft && (
                <SelectItem value={String(DRAFT_SIDE)}>{t('versions.draftOption')}</SelectItem>
              )}
              {versionItems(numbers)}
            </SelectContent>
          </Select>
        </FormField>
        {stats && (
          <p className="pb-2 text-xs">
            <span className="font-medium text-emerald-600 tabular dark:text-emerald-400">+{stats.added}</span>{' '}
            <span className="font-medium text-rose-600 tabular dark:text-rose-400">−{stats.removed}</span>
          </p>
        )}
      </div>
      {!ready ? (
        <p className="text-sm text-muted-foreground">{t('versions.pickTwo')}</p>
      ) : diff.isError ? (
        <ErrorState error={diff.error} onRetry={() => void diff.refetch()} compact />
      ) : (
        <DiffView
          original={diff.data?.fromContent ?? ''}
          modified={diff.data?.toContent ?? ''}
          language={formatLanguage(item.format)}
          height={380}
        />
      )}
    </div>
  );
}
