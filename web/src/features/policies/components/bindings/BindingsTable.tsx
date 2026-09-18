import { Trash2Icon } from 'lucide-react';
import { useMemo, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';

import { PERMISSIONS } from '@/app/auth/permissions';
import { PermissionButton } from '@/app/auth/PermissionGate';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { DataTable, type DataTableColumn } from '@/components/data-table';
import { TimeAgo } from '@/components/TimeAgo';
import { Button } from '@/components/ui/button';
import { type PolicyBinding } from '@/gen/spinneret/v1/policy_admin_pb';

import { bindingPermissionSite } from '../../selectors';
import { useDeleteBinding } from '../../usePolicyMutations';
import { KindBadge, LevelBadge } from '../labels';

export interface BindingsTableProps {
  bindings: readonly PolicyBinding[] | undefined;
  isLoading?: boolean;
  isFetching?: boolean;
  error?: unknown;
  onRetry?: () => void;
  /** Show kind and policy columns (namespace-wide view). */
  showPolicy?: boolean;
  onOpenPolicy?: (policyId: string) => void;
  emptyTitle: string;
  emptyDescription?: string;
  emptyAction?: ReactNode;
  toolbar?: ReactNode;
}

function Dash({ value }: { value: string }) {
  return value ? (
    <span className="font-mono text-xs">{value}</span>
  ) : (
    <span className="text-muted-foreground">—</span>
  );
}

/** Policy bindings with delete (policy:publish on the binding's site). */
export function BindingsTable({
  bindings,
  isLoading,
  isFetching,
  error,
  onRetry,
  showPolicy = false,
  onOpenPolicy,
  emptyTitle,
  emptyDescription,
  emptyAction,
  toolbar,
}: BindingsTableProps) {
  const { t } = useTranslation('policies');
  const [deleting, setDeleting] = useState<PolicyBinding | null>(null);
  const remove = useDeleteBinding();

  const columns = useMemo<DataTableColumn<PolicyBinding>[]>(() => {
    const cols: DataTableColumn<PolicyBinding>[] = [];
    if (showPolicy) {
      cols.push(
        {
          id: 'kind',
          header: t('bindings.columns.kind'),
          meta: { label: t('bindings.columns.kind') },
          cell: ({ row }) => <KindBadge kind={row.original.kind} />,
        },
        {
          id: 'policy',
          header: t('bindings.columns.policy'),
          meta: { label: t('bindings.columns.policy') },
          cell: ({ row }) =>
            onOpenPolicy ? (
              <Button
                variant="link"
                size="sm"
                className="h-auto p-0 font-mono text-xs"
                onClick={() => onOpenPolicy(row.original.policyId)}
              >
                {row.original.policyName}
              </Button>
            ) : (
              <span className="font-mono text-xs">{row.original.policyName}</span>
            ),
        },
      );
    }
    cols.push(
      {
        id: 'level',
        header: t('bindings.columns.level'),
        meta: { label: t('bindings.columns.level') },
        cell: ({ row }) => <LevelBadge level={row.original.level} />,
      },
      {
        id: 'site',
        header: t('bindings.columns.site'),
        meta: { label: t('bindings.columns.site') },
        cell: ({ row }) => <Dash value={row.original.site} />,
      },
      {
        id: 'client',
        header: t('bindings.columns.client'),
        meta: { label: t('bindings.columns.client') },
        cell: ({ row }) => <Dash value={row.original.client} />,
      },
      {
        id: 'endpointGroup',
        header: t('bindings.columns.endpointGroup'),
        meta: { label: t('bindings.columns.endpointGroup') },
        cell: ({ row }) => <Dash value={row.original.endpointGroup} />,
      },
      {
        id: 'createdAt',
        header: t('bindings.columns.createdAt'),
        meta: { label: t('bindings.columns.createdAt') },
        cell: ({ row }) => <TimeAgo value={row.original.createdAt} />,
      },
      {
        id: 'actions',
        header: () => <span className="sr-only">{t('bindings.columns.actions')}</span>,
        enableHiding: false,
        meta: { align: 'right' },
        cell: ({ row }) => (
          <PermissionButton
            permission={PERMISSIONS.policyPublish}
            site={bindingPermissionSite(row.original.site)}
            variant="ghost"
            size="icon-sm"
            aria-label={t('bindings.remove')}
            title={t('bindings.remove')}
            onClick={() => setDeleting(row.original)}
          >
            <Trash2Icon />
          </PermissionButton>
        ),
      },
    );
    return cols;
  }, [t, showPolicy, onOpenPolicy]);

  const target = deleting
    ? [deleting.site, deleting.client, deleting.endpointGroup].filter(Boolean).join(' / ') ||
      t('levels.namespace')
    : '';

  return (
    <>
      <DataTable
        columns={columns}
        data={bindings}
        getRowId={(b) => b.id}
        isLoading={isLoading}
        isFetching={isFetching}
        error={error}
        onRetry={onRetry}
        emptyTitle={emptyTitle}
        emptyDescription={emptyDescription}
        emptyAction={emptyAction}
        toolbar={toolbar}
      />
      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(open) => !open && setDeleting(null)}
        destructive
        title={t('bindings.removeTitle')}
        description={t('bindings.removeDescription', {
          policy: deleting?.policyName ?? '',
          kind: deleting ? t(`kinds.${deleting.kind}`, { defaultValue: deleting.kind }) : '',
          target,
        })}
        confirmLabel={t('bindings.remove')}
        onConfirm={() => (deleting ? remove.mutateAsync(deleting.id) : undefined)}
      />
    </>
  );
}
