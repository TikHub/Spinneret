import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { PlusIcon, ShieldIcon, Trash2Icon } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import { useAuth, useScopedQueryKey } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';
import { PermissionButton } from '@/app/auth/PermissionGate';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { IdText } from '@/components/CopyButton';
import { EmptyState } from '@/components/EmptyState';
import { ErrorState } from '@/components/ErrorState';
import { TimeAgo } from '@/components/TimeAgo';
import { Badge } from '@/components/ui/badge';
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '@/components/ui/sheet';
import { Skeleton } from '@/components/ui/skeleton';
import { type RoleBinding, type User } from '@/gen/spinneret/v1/auth_pb';
import { accessClient } from '@/lib/clients';

import { FormErrorAlert } from '../FormErrorAlert';
import { RowActionButton } from '../RowActionButton';

import { AddBindingForm } from './AddBindingForm';
import { RoleBadge } from './BindingSummary';

/** Bindings listed per user; more than this is not a realistic tenant setup. */
const BINDINGS_PAGE_SIZE = 500;

export interface BindingsSheetProps {
  user: User | undefined;
  onClose: () => void;
}

/** Side panel listing, adding and deleting the role bindings of one user in the active tenant. */
export function BindingsSheet({ user, onClose }: BindingsSheetProps) {
  const { t } = useTranslation('access');
  const { tenant } = useAuth();
  const tenantName = tenant?.tenant?.displayName || tenant?.tenant?.name || '';
  return (
    <Sheet open={user !== undefined} onOpenChange={(open) => !open && onClose()}>
      <SheetContent className="w-full sm:max-w-xl">
        <SheetHeader>
          <SheetTitle className="flex items-center gap-2">
            <ShieldIcon className="size-4 text-muted-foreground" aria-hidden />
            {t('bindings.title', { username: user?.username ?? '' })}
          </SheetTitle>
          <SheetDescription>{t('bindings.description', { tenant: tenantName })}</SheetDescription>
        </SheetHeader>
        {user && <BindingsPanel key={user.id} user={user} />}
      </SheetContent>
    </Sheet>
  );
}

function BindingsPanel({ user }: { user: User }) {
  const { t } = useTranslation('access');
  const { canInTenant, user: me, refresh } = useAuth();
  const queryClient = useQueryClient();
  const key = useScopedQueryKey();
  const canWrite = canInTenant(PERMISSIONS.userWrite);
  const [adding, setAdding] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<RoleBinding>();

  const query = useQuery({
    queryKey: key('access', 'bindings', user.id),
    queryFn: ({ signal }) =>
      accessClient.listRoleBindings({ userId: user.id, pageSize: BINDINGS_PAGE_SIZE }, { signal }),
  });

  const remove = useMutation({
    mutationFn: (binding: RoleBinding) => accessClient.deleteRoleBinding({ id: binding.id }),
    onSuccess: () => {
      toast.success(t('bindings.deleted', { username: user.username }));
      void queryClient.invalidateQueries({ queryKey: ['access'] });
      // The caller's own permissions (and switchers) change with their bindings.
      if (user.id === me?.id) void refresh();
    },
  });

  const bindings = query.data?.bindings ?? [];

  return (
    <div className="grid gap-3 px-4 pb-4">
      <div className="flex items-center justify-between gap-2">
        <span className="text-sm text-muted-foreground">
          {t('bindings.count', { count: query.data?.total ?? bindings.length })}
        </span>
        {!adding && (
          <PermissionButton
            permission={PERMISSIONS.userWrite}
            tenantLevel
            size="sm"
            onClick={() => setAdding(true)}
          >
            <PlusIcon />
            {t('bindings.add')}
          </PermissionButton>
        )}
      </div>

      {adding && (
        <AddBindingForm user={user} onCancel={() => setAdding(false)} onAdded={() => setAdding(false)} />
      )}

      <FormErrorAlert error={remove.error} />

      {query.isLoading ? (
        <div className="grid gap-2">
          {Array.from({ length: 3 }, (_, i) => (
            <Skeleton key={i} className="h-16 w-full" />
          ))}
        </div>
      ) : query.isError ? (
        <ErrorState error={query.error} onRetry={() => void query.refetch()} compact />
      ) : bindings.length === 0 ? (
        <EmptyState
          compact
          icon={ShieldIcon}
          title={t('bindings.empty')}
          description={t('bindings.emptyDescription')}
        />
      ) : (
        <ul className="grid gap-2">
          {bindings.map((binding) => (
            <BindingItem
              key={binding.id}
              binding={binding}
              canWrite={canWrite}
              onDelete={() => setDeleteTarget(binding)}
            />
          ))}
        </ul>
      )}
      {query.data?.nextPageToken && (
        <p className="text-xs text-muted-foreground">{t('bindings.truncated')}</p>
      )}

      <ConfirmDialog
        open={deleteTarget !== undefined}
        onOpenChange={(open) => {
          if (!open) setDeleteTarget(undefined);
        }}
        destructive
        title={t('bindings.deleteTitle')}
        description={t('bindings.deleteDescription', {
          username: user.username,
          role: t(`roles.names.${deleteTarget?.role ?? 'viewer'}`, { defaultValue: deleteTarget?.role }),
          scope: deleteTarget?.namespace || t('bindings.allNamespaces'),
        })}
        confirmText={user.username}
        confirmLabel={t('common:actions.delete')}
        onConfirm={() => (deleteTarget ? remove.mutateAsync(deleteTarget) : undefined)}
      />
    </div>
  );
}

function BindingItem({
  binding,
  canWrite,
  onDelete,
}: {
  binding: RoleBinding;
  canWrite: boolean;
  onDelete: () => void;
}) {
  const { t } = useTranslation('access');
  return (
    <li className="grid gap-2 rounded-md border p-3">
      <div className="flex items-start justify-between gap-2">
        <div className="flex flex-wrap items-center gap-2">
          <RoleBadge role={binding.role} />
          <span className="text-sm">
            {binding.namespace ? (
              <span className="font-mono">{binding.namespace}</span>
            ) : (
              t('bindings.allNamespaces')
            )}
          </span>
        </div>
        <RowActionButton
          icon={Trash2Icon}
          label={t('bindings.delete')}
          destructive
          allowed={canWrite}
          permission={PERMISSIONS.userWrite}
          onClick={onDelete}
        />
      </div>
      <dl className="grid grid-cols-[7rem_1fr] gap-x-2 gap-y-1 text-xs">
        <dt className="text-muted-foreground">{t('bindings.sites')}</dt>
        <dd className="flex flex-wrap gap-1">
          {binding.sites.length === 0 ? (
            <span>{t('bindings.allSites')}</span>
          ) : (
            binding.sites.map((site) => (
              <Badge key={site} variant="outline" className="font-mono">
                {site}
              </Badge>
            ))
          )}
        </dd>
        <dt className="text-muted-foreground">{t('bindings.extraPermissions')}</dt>
        <dd className="flex flex-wrap gap-1">
          {binding.extraPermissions.length === 0 ? (
            <span className="text-muted-foreground">—</span>
          ) : (
            binding.extraPermissions.map((permission) => (
              <Badge key={permission} variant="secondary" className="font-mono">
                {permission}
              </Badge>
            ))
          )}
        </dd>
        <dt className="text-muted-foreground">{t('bindings.created')}</dt>
        <dd>
          <TimeAgo value={binding.createdAt} past />
        </dd>
        <dt className="text-muted-foreground">{t('bindings.id')}</dt>
        <dd>
          <IdText value={binding.id} />
        </dd>
      </dl>
    </li>
  );
}
