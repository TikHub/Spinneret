import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';

import { DataTable, type DataTableColumn } from '@/components/data-table';
import { type NodeStats } from '@/gen/spinneret/v1/dashboard_pb';
import { formatNumber, formatPercent, toNumber } from '@/lib/format';

export interface NodeStatsTableProps {
  nodes: readonly NodeStats[] | undefined;
  isLoading: boolean;
  error: unknown;
  onRetry: () => void;
}

/** Per-node lease and report counters (client-side sorting). */
export function NodeStatsTable({ nodes, isLoading, error, onRetry }: NodeStatsTableProps) {
  const { t, i18n } = useTranslation();
  const lng = i18n.language;

  const columns = useMemo<DataTableColumn<NodeStats>[]>(
    () => [
      {
        accessorKey: 'node',
        header: t('overview.nodes.node'),
        meta: { label: t('overview.nodes.node') },
        cell: ({ row }) => <span className="font-mono text-xs">{row.original.node}</span>,
      },
      {
        id: 'acquires',
        accessorFn: (n) => toNumber(n.acquires),
        header: t('overview.nodes.acquires'),
        meta: { label: t('overview.nodes.acquires'), align: 'right', className: 'tabular' },
        cell: ({ getValue }) => formatNumber(getValue<number>(), undefined, lng),
      },
      {
        id: 'reports',
        accessorFn: (n) => toNumber(n.reports),
        header: t('overview.nodes.reports'),
        meta: { label: t('overview.nodes.reports'), align: 'right', className: 'tabular' },
        cell: ({ getValue }) => formatNumber(getValue<number>(), undefined, lng),
      },
      {
        id: 'abandoned',
        accessorFn: (n) => toNumber(n.abandoned),
        header: t('overview.nodes.abandoned'),
        meta: { label: t('overview.nodes.abandoned'), align: 'right', className: 'tabular' },
        cell: ({ getValue }) => formatNumber(getValue<number>(), undefined, lng),
      },
      {
        id: 'rejected',
        accessorFn: (n) => toNumber(n.rejected),
        header: t('overview.nodes.rejected'),
        meta: { label: t('overview.nodes.rejected'), align: 'right', className: 'tabular' },
        cell: ({ getValue }) => formatNumber(getValue<number>(), undefined, lng),
      },
      {
        accessorKey: 'unreportedRatio',
        header: t('overview.nodes.unreportedRatio'),
        meta: { label: t('overview.nodes.unreportedRatio'), align: 'right', className: 'tabular' },
        cell: ({ row }) => formatPercent(row.original.unreportedRatio, 1, lng),
      },
    ],
    [t, lng],
  );

  return (
    <DataTable
      columns={columns}
      data={nodes}
      getRowId={(n) => n.node}
      isLoading={isLoading}
      error={error ?? undefined}
      onRetry={onRetry}
      emptyTitle={t('overview.nodes.empty')}
      emptyDescription=""
      enableColumnVisibility={false}
      initialSorting={[{ id: 'acquires', desc: true }]}
      virtualize={false}
      maxHeight={360}
    />
  );
}
