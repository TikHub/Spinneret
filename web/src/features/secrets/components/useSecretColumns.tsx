import { EyeIcon, PencilIcon, Trash2Icon } from 'lucide-react';
import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';

import { PERMISSIONS } from '@/app/auth/permissions';
import { PermissionButton } from '@/app/auth/PermissionGate';
import { type DataTableColumn } from '@/components/data-table';
import { TimeAgo } from '@/components/TimeAgo';
import { Badge } from '@/components/ui/badge';
import { type SecretInfo } from '@/gen/spinneret/v1/secret_admin_pb';

import { splitSecretPath } from '../secretPath';
import { ExpiryCell } from './ExpiryCell';

const VISIBLE_TAGS = 3;

export interface SecretRowActions {
  onReveal: (secret: SecretInfo) => void;
  onEdit: (secret: SecretInfo) => void;
  onDelete: (secret: SecretInfo) => void;
}

/** Columns of the secrets table. */
export function useSecretColumns({
  onReveal,
  onEdit,
  onDelete,
}: SecretRowActions): DataTableColumn<SecretInfo>[] {
  const { t } = useTranslation('secrets');
  return useMemo<DataTableColumn<SecretInfo>[]>(
    () => [
      {
        id: 'path',
        header: t('fields.path'),
        enableHiding: false,
        meta: { label: t('fields.path'), className: 'max-w-96' },
        cell: ({ row }) => {
          const { folder, name } = splitSecretPath(row.original.path);
          return (
            <span className="grid min-w-0">
              <span className="truncate font-mono text-xs" title={row.original.path}>
                <span className="text-muted-foreground">{folder}</span>
                <span className="font-medium">{name}</span>
              </span>
              {row.original.description && (
                <span className="truncate text-xs text-muted-foreground" title={row.original.description}>
                  {row.original.description}
                </span>
              )}
            </span>
          );
        },
      },
      {
        id: 'value',
        header: t('fields.maskedValue'),
        meta: { label: t('fields.maskedValue') },
        cell: ({ row }) => <span className="font-mono text-xs">{row.original.maskedValue || '—'}</span>,
      },
      {
        id: 'version',
        header: t('fields.version'),
        meta: { label: t('fields.version'), align: 'right' },
        cell: ({ row }) => <span className="tabular">v{row.original.currentVersion}</span>,
      },
      {
        id: 'tags',
        header: t('fields.tags'),
        meta: { label: t('fields.tags') },
        cell: ({ row }) => {
          const tags = row.original.tags;
          if (tags.length === 0) return <span className="text-muted-foreground">—</span>;
          return (
            <span className="flex flex-wrap gap-1" title={tags.join(', ')}>
              {tags.slice(0, VISIBLE_TAGS).map((tag) => (
                <Badge key={tag} variant="outline">
                  {tag}
                </Badge>
              ))}
              {tags.length > VISIBLE_TAGS && <Badge variant="muted">+{tags.length - VISIBLE_TAGS}</Badge>}
            </span>
          );
        },
      },
      {
        id: 'expiresAt',
        header: t('fields.expiresAt'),
        meta: { label: t('fields.expiresAt') },
        cell: ({ row }) => <ExpiryCell value={row.original.expiresAt} />,
      },
      {
        id: 'lastAccessed',
        header: t('fields.lastAccessed'),
        meta: { label: t('fields.lastAccessed') },
        cell: ({ row }) => (
          <TimeAgo value={row.original.lastAccessedAt} fallback={t('common:time.never')} past />
        ),
      },
      {
        id: 'updatedAt',
        header: t('fields.updatedAt'),
        meta: { label: t('fields.updatedAt') },
        cell: ({ row }) => <TimeAgo value={row.original.updatedAt} past />,
      },
      {
        id: 'actions',
        header: () => <span className="sr-only">{t('common:actions.more')}</span>,
        enableHiding: false,
        meta: { align: 'right' },
        cell: ({ row }) => (
          <span className="inline-flex items-center gap-0.5">
            <PermissionButton
              permission={PERMISSIONS.secretReveal}
              variant="ghost"
              size="icon-sm"
              aria-label={t('actions.reveal')}
              title={t('actions.reveal')}
              onClick={() => onReveal(row.original)}
            >
              <EyeIcon />
            </PermissionButton>
            <PermissionButton
              permission={PERMISSIONS.secretWrite}
              variant="ghost"
              size="icon-sm"
              aria-label={t('actions.edit')}
              title={t('actions.edit')}
              onClick={() => onEdit(row.original)}
            >
              <PencilIcon />
            </PermissionButton>
            <PermissionButton
              permission={PERMISSIONS.secretWrite}
              variant="ghost"
              size="icon-sm"
              className="text-destructive hover:text-destructive"
              aria-label={t('common:actions.delete')}
              title={t('common:actions.delete')}
              onClick={() => onDelete(row.original)}
            >
              <Trash2Icon />
            </PermissionButton>
          </span>
        ),
      },
    ],
    [t, onReveal, onEdit, onDelete],
  );
}
