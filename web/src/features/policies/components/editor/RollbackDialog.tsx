import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';

import { ConfirmDialog } from '@/components/ConfirmDialog';
import { useCursorPagination } from '@/components/data-table';
import { FormField } from '@/components/ui/form';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { Textarea } from '@/components/ui/textarea';
import { type Policy } from '@/gen/spinneret/v1/policy_admin_pb';
import { formatDateTime } from '@/lib/time';

import { useRollbackPolicy } from '../../usePolicyMutations';
import { usePolicyVersions } from '../../usePolicyQueries';

export interface RollbackDialogProps {
  policy: Policy;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Preselected version (e.g. from the versions table). */
  initialVersion?: number;
}

const ROLLBACK_PAGE_SIZE = 100;

/** Republishes the YAML of an older version as a new version. */
export function RollbackDialog({ policy, open, onOpenChange, initialVersion }: RollbackDialogProps) {
  const { t } = useTranslation('policies');
  const pager = useCursorPagination({ pageSize: ROLLBACK_PAGE_SIZE, resetOn: policy.id });
  const versions = usePolicyVersions(open ? policy.id : '', pager);
  const rollback = useRollbackPolicy();
  const [version, setVersion] = useState(initialVersion ? String(initialVersion) : '');
  const [comment, setComment] = useState('');
  const [wasOpen, setWasOpen] = useState(open);
  if (open !== wasOpen) {
    setWasOpen(open);
    if (open) {
      setVersion(initialVersion ? String(initialVersion) : '');
      setComment('');
    }
  }

  const options = useMemo(
    () => (versions.data?.versions ?? []).filter((v) => v.version !== policy.currentVersion),
    [versions.data, policy.currentVersion],
  );

  return (
    <ConfirmDialog
      open={open}
      onOpenChange={onOpenChange}
      title={t('rollback.title', { name: policy.name })}
      description={t('rollback.description', { current: policy.currentVersion })}
      confirmLabel={t('rollback.confirm')}
      confirmDisabled={version === ''}
      onConfirm={() =>
        rollback.mutateAsync({
          id: policy.id,
          version: Number(version),
          comment: comment.trim() || t('rollback.defaultComment', { version }),
        })
      }
    >
      <div className="grid gap-3">
        <FormField label={t('fields.version')} required>
          <Select value={version} onValueChange={setVersion} disabled={versions.isLoading}>
            <SelectTrigger className="w-full">
              <SelectValue placeholder={versions.isLoading ? t('rollback.loading') : t('rollback.pick')} />
            </SelectTrigger>
            <SelectContent>
              {options.map((v) => (
                <SelectItem key={v.version} value={String(v.version)}>
                  <span className="font-mono">v{v.version}</span>
                  <span className="text-muted-foreground">
                    {formatDateTime(v.createdAt, false)}
                    {v.comment && ` · ${v.comment}`}
                  </span>
                </SelectItem>
              ))}
              {initialVersion !== undefined && !options.some((v) => v.version === initialVersion) && (
                <SelectItem value={String(initialVersion)}>v{initialVersion}</SelectItem>
              )}
            </SelectContent>
          </Select>
        </FormField>
        {versions.isError && <p className="text-xs text-destructive">{t('rollback.loadFailed')}</p>}
        <FormField label={t('fields.comment')}>
          <Textarea value={comment} onChange={(e) => setComment(e.target.value)} maxLength={1024} rows={2} />
        </FormField>
      </div>
    </ConfirmDialog>
  );
}
