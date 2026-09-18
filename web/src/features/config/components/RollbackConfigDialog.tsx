import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import { DiffView } from '@/components/editor/DiffView';
import { ErrorState } from '@/components/ErrorState';
import { FormField } from '@/components/ui/form';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { Skeleton } from '@/components/ui/skeleton';
import { Textarea } from '@/components/ui/textarea';
import { type ConfigItemInfo, type ConfigVersion } from '@/gen/spinneret/v1/config_admin_pb';
import { configAdminClient } from '@/lib/clients';
import { formatDateTime } from '@/lib/time';

import { formatLanguage, itemLabel, MAX_CONFIG_COMMENT_LENGTH } from '../configModel';
import { useRecentConfigVersions } from '../useConfigApi';
import { WideConfirmDialog } from './WideConfirmDialog';

export interface RollbackConfigDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  item: ConfigItemInfo;
  /** Preselected version (e.g. from the versions table, possibly older than the picker's page). */
  preselected?: ConfigVersion;
  onRolledBack: (item: ConfigItemInfo | undefined) => void;
}

/** Rollback: pick an older version, preview it against the current one and republish it. */
export function RollbackConfigDialog({
  open,
  onOpenChange,
  item,
  preselected,
  onRolledBack,
}: RollbackConfigDialogProps) {
  const { t } = useTranslation('config');
  const [version, setVersion] = useState<number>();
  const [comment, setComment] = useState('');
  const [wasOpen, setWasOpen] = useState(open);
  if (open !== wasOpen) {
    setWasOpen(open);
    if (open) {
      setVersion(
        preselected && preselected.version !== item.currentVersion ? preselected.version : undefined,
      );
      setComment('');
    }
  }
  const versions = useRecentConfigVersions(item.id, open);
  const recent = versions.data?.versions ?? [];
  const listed =
    preselected && !recent.some((v) => v.version === preselected.version) ? [...recent, preselected] : recent;
  const candidates = listed.filter((v) => v.version !== item.currentVersion);
  const selected = candidates.find((v) => v.version === version);

  const rollback = async () => {
    if (version === undefined) return;
    const res = await configAdminClient.rollbackConfig({ id: item.id, version, comment: comment.trim() });
    toast.success(t('rollback.success', { label: itemLabel(item), version, newVersion: res.version }));
    onRolledBack(res.item);
  };

  return (
    <WideConfirmDialog
      open={open}
      onOpenChange={onOpenChange}
      title={t('rollback.title', { label: itemLabel(item) })}
      description={t('rollback.description', { next: item.currentVersion + 1 })}
      confirmLabel={version === undefined ? t('rollback.confirmNone') : t('rollback.confirm', { version })}
      destructive
      confirmDisabled={selected === undefined}
      onConfirm={rollback}
    >
      <div className="grid gap-3">
        <div className="grid gap-3 sm:grid-cols-[16rem_1fr]">
          <FormField label={t('rollback.version')} required>
            <Select
              value={version === undefined ? '' : String(version)}
              onValueChange={(v) => setVersion(Number(v))}
              disabled={versions.isLoading || candidates.length === 0}
            >
              <SelectTrigger className="w-full">
                <SelectValue placeholder={t('rollback.pickVersion')} />
              </SelectTrigger>
              <SelectContent>
                {candidates.map((v) => (
                  <SelectItem key={v.version} value={String(v.version)}>
                    <span className="tabular">v{v.version}</span>
                    <span className="text-muted-foreground">{formatDateTime(v.publishedAt, false)}</span>
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </FormField>
          <FormField label={t('fields.comment')}>
            <Textarea
              value={comment}
              rows={1}
              maxLength={MAX_CONFIG_COMMENT_LENGTH}
              placeholder={version === undefined ? undefined : t('rollback.commentPlaceholder', { version })}
              onChange={(e) => setComment(e.target.value)}
            />
          </FormField>
        </div>
        {versions.isError ? (
          <ErrorState error={versions.error} onRetry={() => void versions.refetch()} compact />
        ) : versions.isLoading ? (
          <Skeleton className="h-72 w-full" />
        ) : candidates.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t('rollback.noVersions')}</p>
        ) : selected ? (
          <div className="grid gap-1">
            <p className="text-xs text-muted-foreground">
              {t('rollback.previewLabel', { current: item.currentVersion, version: selected.version })}
              {selected.comment && <span className="ml-2 italic">“{selected.comment}”</span>}
            </p>
            <DiffView
              original={item.publishedContent}
              modified={selected.content}
              language={formatLanguage(item.format)}
              height={320}
            />
          </div>
        ) : (
          <p className="text-sm text-muted-foreground">{t('rollback.pickHint')}</p>
        )}
      </div>
    </WideConfirmDialog>
  );
}
