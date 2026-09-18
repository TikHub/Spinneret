import { EyeIcon, GitCompareIcon, HistoryIcon } from 'lucide-react';
import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';

import { useAuth } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';
import { PermissionButton } from '@/app/auth/PermissionGate';
import { CopyButton } from '@/components/CopyButton';
import { DataTable, useCursorPagination, type DataTableColumn } from '@/components/data-table';
import { CodeEditor } from '@/components/editor/CodeEditor';
import { TimeAgo } from '@/components/TimeAgo';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog';
import { SimpleTooltip } from '@/components/ui/tooltip';
import { type Policy, type PolicyVersion } from '@/gen/spinneret/v1/policy_admin_pb';

import { usePolicyVersions, type DiffSides } from '../../usePolicyQueries';
import { RollbackDialog } from '../editor/RollbackDialog';
import { defaultDiffSides } from '../../selectors';
import { VersionDiff } from './VersionDiff';

export interface VersionsTabProps {
  policy: Policy;
}

/** Published versions with YAML view, diff and rollback. */
export function VersionsTab({ policy }: VersionsTabProps) {
  const { t } = useTranslation('policies');
  const { tenantId, namespaceName } = useAuth();
  const pager = useCursorPagination({ resetOn: [tenantId, namespaceName, policy.id] });
  const versions = usePolicyVersions(policy.id, pager);
  const [viewing, setViewing] = useState<PolicyVersion | null>(null);
  const [rollbackTo, setRollbackTo] = useState<number | undefined>();
  const [sides, setSides] = useState<DiffSides | undefined>(() => defaultDiffSides(policy));

  const columns = useMemo<DataTableColumn<PolicyVersion>[]>(
    () => [
      {
        id: 'version',
        header: t('versions.columns.version'),
        meta: { label: t('versions.columns.version') },
        cell: ({ row }) => (
          <span className="inline-flex items-center gap-2">
            <span className="font-mono tabular">v{row.original.version}</span>
            {row.original.version === policy.currentVersion && (
              <Badge variant="secondary">{t('versions.current')}</Badge>
            )}
          </span>
        ),
      },
      {
        id: 'comment',
        header: t('versions.columns.comment'),
        meta: { label: t('versions.columns.comment'), className: 'max-w-md' },
        cell: ({ row }) =>
          row.original.comment ? (
            <span className="line-clamp-2 break-words">{row.original.comment}</span>
          ) : (
            <span className="text-muted-foreground">—</span>
          ),
      },
      {
        id: 'createdBy',
        header: t('versions.columns.createdBy'),
        meta: { label: t('versions.columns.createdBy') },
        cell: ({ row }) => <span className="font-mono text-xs">{row.original.createdBy || '—'}</span>,
      },
      {
        id: 'createdAt',
        header: t('versions.columns.createdAt'),
        meta: { label: t('versions.columns.createdAt') },
        cell: ({ row }) => <TimeAgo value={row.original.createdAt} />,
      },
      {
        id: 'actions',
        header: () => <span className="sr-only">{t('versions.columns.actions')}</span>,
        enableHiding: false,
        meta: { align: 'right' },
        cell: ({ row }) => {
          const v = row.original;
          const isCurrent = v.version === policy.currentVersion;
          return (
            <div className="flex justify-end gap-1">
              <SimpleTooltip content={t('versions.view')}>
                <Button
                  variant="ghost"
                  size="icon-sm"
                  aria-label={t('versions.viewVersion', { version: v.version })}
                  onClick={() => setViewing(v)}
                >
                  <EyeIcon />
                </Button>
              </SimpleTooltip>
              <SimpleTooltip
                content={policy.hasDraft ? t('versions.compareDraft') : t('versions.compareCurrent')}
              >
                <Button
                  variant="ghost"
                  size="icon-sm"
                  disabled={isCurrent && !policy.hasDraft}
                  aria-label={t('versions.compareVersion', { version: v.version })}
                  onClick={() =>
                    setSides({ from: v.version, to: policy.hasDraft ? 0 : policy.currentVersion })
                  }
                >
                  <GitCompareIcon />
                </Button>
              </SimpleTooltip>
              {!isCurrent && (
                <PermissionButton
                  permission={PERMISSIONS.policyPublish}
                  variant="ghost"
                  size="icon-sm"
                  aria-label={t('versions.rollbackTo', { version: v.version })}
                  title={t('versions.rollbackTo', { version: v.version })}
                  onClick={() => setRollbackTo(v.version)}
                >
                  <HistoryIcon />
                </PermissionButton>
              )}
            </div>
          );
        },
      },
    ],
    [t, policy.currentVersion, policy.hasDraft],
  );

  return (
    <div className="grid gap-4">
      <DataTable
        columns={columns}
        data={versions.data?.versions}
        getRowId={(v) => String(v.version)}
        isLoading={versions.isLoading}
        isFetching={versions.isFetching}
        error={versions.error}
        onRetry={() => void versions.refetch()}
        emptyTitle={t('versions.empty')}
        emptyDescription={t('versions.emptyDescription')}
        enableColumnVisibility={false}
        pagination={{ pager, nextPageToken: versions.data?.nextPageToken, total: versions.data?.total }}
      />
      {policy.currentVersion > 0 && <VersionDiff policy={policy} sides={sides} onSidesChange={setSides} />}

      <Dialog open={viewing !== null} onOpenChange={(open) => !open && setViewing(null)}>
        <DialogContent className="sm:max-w-3xl">
          <DialogHeader>
            <DialogTitle>
              {t('versions.yamlTitle', { name: policy.name, version: viewing?.version ?? 0 })}
            </DialogTitle>
            <DialogDescription>{viewing?.comment || t('versions.noComment')}</DialogDescription>
          </DialogHeader>
          {viewing && (
            <>
              <CodeEditor
                value={viewing.specYaml}
                readOnly
                height={460}
                path={`policy-${policy.id}-v${viewing.version}.yaml`}
                aria-label={t('versions.yamlTitle', { name: policy.name, version: viewing.version })}
              />
              <div className="flex justify-end">
                <CopyButton
                  value={viewing.specYaml}
                  label={t('versions.copyYaml')}
                  variant="outline"
                  className="size-8"
                />
              </div>
            </>
          )}
        </DialogContent>
      </Dialog>

      <RollbackDialog
        policy={policy}
        open={rollbackTo !== undefined}
        initialVersion={rollbackTo}
        onOpenChange={(open) => !open && setRollbackTo(undefined)}
      />
    </div>
  );
}
