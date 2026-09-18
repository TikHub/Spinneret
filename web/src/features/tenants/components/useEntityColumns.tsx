import { type Timestamp } from '@bufbuild/protobuf/wkt';
import { useMemo, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';

import { IdText } from '@/components/CopyButton';
import { type DataTableColumn } from '@/components/data-table';
import { TimeAgo } from '@/components/TimeAgo';
import { Badge } from '@/components/ui/badge';

/** Fields shared by tenants and namespaces. */
export interface SlugEntity {
  id: string;
  name: string;
  displayName: string;
  description: string;
  createdAt?: Timestamp;
  updatedAt?: Timestamp;
}

export interface EntityColumnOptions<T extends SlugEntity> {
  /** ID of the active tenant or namespace (marked in the table). */
  activeId: string | undefined;
  /** Row action buttons. */
  renderActions: (row: T) => ReactNode;
}

/** Columns of the tenants and namespaces tables. */
export function useEntityColumns<T extends SlugEntity>({
  activeId,
  renderActions,
}: EntityColumnOptions<T>): DataTableColumn<T>[] {
  const { t } = useTranslation('tenants');
  return useMemo<DataTableColumn<T>[]>(
    () => [
      {
        id: 'name',
        accessorKey: 'name',
        header: t('columns.name'),
        enableHiding: false,
        meta: { label: t('columns.name') },
        cell: ({ row }) => (
          <span className="flex items-center gap-1.5">
            <span className="font-mono text-sm font-medium">{row.original.name}</span>
            {row.original.id === activeId && <Badge variant="secondary">{t('active')}</Badge>}
          </span>
        ),
      },
      {
        id: 'displayName',
        accessorKey: 'displayName',
        header: t('columns.displayName'),
        meta: { label: t('columns.displayName'), className: 'max-w-56 truncate' },
        cell: ({ row }) => row.original.displayName || <span className="text-muted-foreground">—</span>,
      },
      {
        id: 'description',
        accessorKey: 'description',
        header: t('columns.description'),
        enableSorting: false,
        meta: { label: t('columns.description'), className: 'max-w-80 truncate' },
        cell: ({ row }) =>
          row.original.description ? (
            <span title={row.original.description}>{row.original.description}</span>
          ) : (
            <span className="text-muted-foreground">—</span>
          ),
      },
      {
        id: 'id',
        accessorKey: 'id',
        header: t('columns.id'),
        meta: { label: t('columns.id') },
        cell: ({ row }) => <IdText value={row.original.id} />,
      },
      {
        id: 'createdAt',
        accessorFn: (row) => Number(row.createdAt?.seconds ?? 0n),
        header: t('columns.createdAt'),
        meta: { label: t('columns.createdAt') },
        cell: ({ row }) => <TimeAgo value={row.original.createdAt} past />,
      },
      {
        id: 'updatedAt',
        accessorFn: (row) => Number(row.updatedAt?.seconds ?? 0n),
        header: t('columns.updatedAt'),
        meta: { label: t('columns.updatedAt') },
        cell: ({ row }) => <TimeAgo value={row.original.updatedAt} past />,
      },
      {
        id: 'actions',
        header: () => <span className="sr-only">{t('columns.actions')}</span>,
        enableSorting: false,
        enableHiding: false,
        meta: { align: 'right' },
        cell: ({ row }) => (
          <span className="inline-flex items-center gap-0.5">{renderActions(row.original)}</span>
        ),
      },
    ],
    [t, activeId, renderActions],
  );
}
