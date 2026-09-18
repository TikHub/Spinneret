import { Link } from '@tanstack/react-router';
import { EllipsisIcon, EyeIcon, PencilIcon, PlayIcon, Trash2Icon } from 'lucide-react';
import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';

import { type DataTableColumn } from '@/components/data-table';
import { TimeAgo } from '@/components/TimeAgo';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import { type IdentityType } from '@/gen/spinneret/v1/identity_admin_pb';
import { formatNumber } from '@/lib/format';

import { FieldSummary } from './FieldSummary';

export type IdentityTypeAction = 'edit' | 'preview' | 'delete';

export interface IdentityTypeColumnsOptions {
  onAction: (action: IdentityTypeAction, type: IdentityType) => void;
  /** Whether the user may edit types of a site (site:write). */
  canWrite: (site: string) => boolean;
}

/** Columns of the identity types table. */
export function useIdentityTypeColumns({
  onAction,
  canWrite,
}: IdentityTypeColumnsOptions): DataTableColumn<IdentityType>[] {
  const { t, i18n } = useTranslation('identity-types');
  const lng = i18n.language;
  return useMemo<DataTableColumn<IdentityType>[]>(
    () => [
      {
        id: 'name',
        header: t('columns.name'),
        enableSorting: false,
        enableHiding: false,
        meta: { label: t('columns.name') },
        cell: ({ row }) => (
          <div className="grid max-w-64 gap-0.5 whitespace-normal">
            <span className="font-mono text-xs font-medium">{row.original.name}</span>
            {row.original.description && (
              <span className="truncate text-xs text-muted-foreground" title={row.original.description}>
                {row.original.description}
              </span>
            )}
          </div>
        ),
      },
      {
        id: 'site',
        header: t('columns.site'),
        enableSorting: false,
        meta: { label: t('columns.site') },
        cell: ({ row }) => (
          <span>
            {row.original.site} <span className="text-muted-foreground">· {row.original.client}</span>
          </span>
        ),
      },
      {
        id: 'fields',
        header: t('columns.fields'),
        enableSorting: false,
        meta: { label: t('columns.fields') },
        cell: ({ row }) => <FieldSummary fields={row.original.fields} />,
      },
      {
        id: 'uniqueBy',
        header: t('columns.uniqueBy'),
        enableSorting: false,
        meta: { label: t('columns.uniqueBy'), className: 'max-w-48 truncate font-mono text-xs' },
        cell: ({ row }) => row.original.uniqueBy.join(', ') || '—',
      },
      {
        id: 'activation',
        header: t('columns.activation'),
        enableSorting: false,
        meta: { label: t('columns.activation') },
        cell: ({ row }) => (
          <Badge
            variant={row.original.activation === 'immediate' ? 'secondary' : 'outline'}
            className="font-normal"
          >
            {t(`activation.${row.original.activation || 'probe'}`, { defaultValue: row.original.activation })}
          </Badge>
        ),
      },
      {
        id: 'version',
        header: t('columns.version'),
        enableSorting: false,
        meta: { label: t('columns.version'), align: 'right', className: 'tabular' },
        cell: ({ row }) => `v${row.original.version}`,
      },
      {
        id: 'identityCount',
        header: t('columns.identities'),
        enableSorting: false,
        meta: { label: t('columns.identities'), align: 'right', className: 'tabular' },
        cell: ({ row }) => (
          <Link
            to="/identities"
            search={{ site: row.original.site, type: row.original.name }}
            className="hover:underline"
          >
            {formatNumber(row.original.identityCount, undefined, lng)}
          </Link>
        ),
      },
      {
        id: 'updatedAt',
        header: t('columns.updated'),
        enableSorting: false,
        meta: { label: t('columns.updated') },
        cell: ({ row }) => <TimeAgo value={row.original.updatedAt} past />,
      },
      {
        id: 'actions',
        header: () => <span className="sr-only">{t('columns.actions')}</span>,
        enableSorting: false,
        enableHiding: false,
        meta: { align: 'right' },
        cell: ({ row }) => {
          const type = row.original;
          const writable = canWrite(type.site);
          return (
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="ghost" size="icon-sm" className="size-7" aria-label={t('columns.actions')}>
                  <EllipsisIcon />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                <DropdownMenuItem onSelect={() => onAction('edit', type)}>
                  {writable ? <PencilIcon /> : <EyeIcon />}
                  {writable ? t('common:actions.edit') : t('actions.view')}
                </DropdownMenuItem>
                <DropdownMenuItem onSelect={() => onAction('preview', type)}>
                  <PlayIcon />
                  {t('actions.preview')}
                </DropdownMenuItem>
                <DropdownMenuSeparator />
                <DropdownMenuItem
                  variant="destructive"
                  disabled={!writable}
                  title={writable ? undefined : t('common:permission.missing', { permission: 'site:write' })}
                  onSelect={() => onAction('delete', type)}
                >
                  <Trash2Icon />
                  {t('common:actions.delete')}
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          );
        },
      },
    ],
    [t, lng, onAction, canWrite],
  );
}
