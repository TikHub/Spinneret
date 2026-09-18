import { EyeIcon, GitCompareArrowsIcon, HistoryIcon, Undo2Icon } from 'lucide-react';
import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';

import { useAuth } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';
import { PermissionButton } from '@/app/auth/PermissionGate';
import { DataTable, useCursorPagination, type DataTableColumn } from '@/components/data-table';
import { CodeEditor } from '@/components/editor/CodeEditor';
import { TimeAgo } from '@/components/TimeAgo';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog';
import { SimpleTooltip } from '@/components/ui/tooltip';
import { type ConfigItemInfo, type ConfigVersion } from '@/gen/spinneret/v1/config_admin_pb';

import { formatLanguage, itemLabel } from '../configModel';
import { useConfigVersions } from '../useConfigApi';
import { VersionCompare, type CompareSides } from './VersionCompare';

export interface ConfigVersionsTabProps {
  item: ConfigItemInfo;
  onRollback: (version: ConfigVersion) => void;
}

/** Published versions of an item: list, content view, diff and rollback shortcut. */
export function ConfigVersionsTab({ item, onRollback }: ConfigVersionsTabProps) {
  const { t } = useTranslation('config');
  const { tenantId, namespaceName } = useAuth();
  const pager = useCursorPagination({ pageSize: 25, resetOn: [tenantId, namespaceName, item.id] });
  const versions = useConfigVersions(item.id, pager);
  const [viewing, setViewing] = useState<ConfigVersion>();
  const [sides, setSides] = useState<CompareSides>();
  const rows = versions.data?.versions;

  const columns = useMemo<DataTableColumn<ConfigVersion>[]>(
    () => [
      {
        id: 'version',
        header: t('versions.version'),
        meta: { label: t('versions.version') },
        cell: ({ row }) => (
          <span className="inline-flex items-center gap-1.5">
            <span className="font-mono tabular">v{row.original.version}</span>
            {row.original.version === item.currentVersion && (
              <Badge variant="secondary">{t('versions.current')}</Badge>
            )}
          </span>
        ),
      },
      {
        id: 'comment',
        header: t('fields.comment'),
        meta: { label: t('fields.comment'), className: 'max-w-80 whitespace-normal' },
        cell: ({ row }) => (
          <span className="grid gap-0.5">
            <span className="line-clamp-2 break-words">{row.original.comment || '—'}</span>
            {row.original.sourceVersion > 0 && (
              <span className="inline-flex items-center gap-1 text-xs text-muted-foreground">
                <HistoryIcon className="size-3" aria-hidden />
                {t('versions.rollbackOf', { version: row.original.sourceVersion })}
              </span>
            )}
          </span>
        ),
      },
      {
        id: 'publishedBy',
        header: t('versions.publishedBy'),
        meta: { label: t('versions.publishedBy') },
        cell: ({ row }) => <span className="font-mono text-xs">{row.original.publishedBy || '—'}</span>,
      },
      {
        id: 'publishedAt',
        header: t('versions.publishedAt'),
        meta: { label: t('versions.publishedAt') },
        cell: ({ row }) => <TimeAgo value={row.original.publishedAt} past />,
      },
      {
        id: 'actions',
        header: () => <span className="sr-only">{t('common:actions.more')}</span>,
        enableHiding: false,
        meta: { align: 'right' },
        cell: ({ row }) => {
          const v = row.original;
          return (
            <span className="inline-flex items-center gap-1">
              <SimpleTooltip content={t('versions.view')}>
                <Button
                  variant="ghost"
                  size="icon-sm"
                  aria-label={t('versions.view')}
                  onClick={() => setViewing(v)}
                >
                  <EyeIcon />
                </Button>
              </SimpleTooltip>
              <SimpleTooltip content={t('versions.compareWithCurrent')}>
                <Button
                  variant="ghost"
                  size="icon-sm"
                  aria-label={t('versions.compareWithCurrent')}
                  disabled={v.version === item.currentVersion}
                  onClick={() => setSides({ from: v.version, to: item.currentVersion })}
                >
                  <GitCompareArrowsIcon />
                </Button>
              </SimpleTooltip>
              <PermissionButton
                permission={PERMISSIONS.configPublish}
                variant="ghost"
                size="icon-sm"
                aria-label={t('versions.rollbackTo', { version: v.version })}
                title={t('versions.rollbackTo', { version: v.version })}
                disabled={v.version === item.currentVersion}
                onClick={() => onRollback(v)}
              >
                <Undo2Icon />
              </PermissionButton>
            </span>
          );
        },
      },
    ],
    [t, item.currentVersion, onRollback],
  );

  return (
    <div className="grid gap-3">
      <DataTable
        columns={columns}
        data={rows}
        getRowId={(v) => String(v.version)}
        isLoading={versions.isLoading}
        isFetching={versions.isFetching}
        error={versions.error}
        onRetry={() => void versions.refetch()}
        emptyTitle={t('versions.empty')}
        emptyDescription={item.hasDraft ? t('versions.emptyWithDraft') : undefined}
        enableColumnVisibility={false}
        pagination={{ pager, nextPageToken: versions.data?.nextPageToken, total: versions.data?.total }}
      />
      {rows && rows.length > 0 && (
        <VersionCompare item={item} versions={rows} sides={sides} onSidesChange={setSides} />
      )}
      <Dialog open={viewing !== undefined} onOpenChange={(open) => !open && setViewing(undefined)}>
        <DialogContent className="sm:max-w-4xl">
          <DialogHeader>
            <DialogTitle>
              {t('versions.viewTitle', { label: itemLabel(item), version: viewing?.version ?? 0 })}
            </DialogTitle>
            <DialogDescription>{viewing?.comment || t('versions.noComment')}</DialogDescription>
          </DialogHeader>
          {viewing && (
            <CodeEditor
              value={viewing.content}
              language={formatLanguage(item.format)}
              readOnly
              height={460}
              path={`config-version-${item.id}-${viewing.version}`}
              aria-label={t('versions.viewTitle', { label: itemLabel(item), version: viewing.version })}
            />
          )}
        </DialogContent>
      </Dialog>
    </div>
  );
}
