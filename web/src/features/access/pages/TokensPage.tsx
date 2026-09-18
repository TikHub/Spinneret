import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { KeySquareIcon, PlusIcon, RefreshCwIcon } from 'lucide-react';
import { useCallback, useId, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import { useAuth, useScopedPlaceholder, useScopedQueryKey } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';
import { PermissionButton, RequirePermission } from '@/app/auth/PermissionGate';
import { LIVE_REFETCH_MS } from '@/app/queryClient';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { DataTable, useCursorPagination } from '@/components/data-table';
import { EmptyState } from '@/components/EmptyState';
import { PageHeader } from '@/components/PageHeader';
import { PageIntro } from '@/components/PageIntro';
import { Button } from '@/components/ui/button';
import { Label } from '@/components/ui/label';
import { Switch } from '@/components/ui/switch';
import { type ApiToken, type CreateTokenResponse } from '@/gen/spinneret/v1/access_admin_pb';
import { accessClient } from '@/lib/clients';
import { useNow } from '@/lib/clock';

import { CreateTokenDialog } from '../components/tokens/CreateTokenDialog';
import { ScopeChips } from '../components/tokens/ScopeChips';
import { TokenSecretDialog, type CreatedTokenSecret } from '../components/tokens/TokenSecretDialog';
import { useTokenColumns } from '../components/tokens/useTokenColumns';

/** API tokens of the active namespace: list, create (plaintext shown once) and revoke. */
export default function TokensPage() {
  const { t } = useTranslation('access');
  const { namespaceName } = useAuth();
  if (!namespaceName) {
    return (
      <>
        <PageHeader title={t('tokens.title')} />
        <EmptyState icon={KeySquareIcon} title={t('tokens.noNamespace')} />
      </>
    );
  }
  return (
    <RequirePermission permission={PERMISSIONS.tokenRead}>
      <TokensContent namespace={namespaceName} />
    </RequirePermission>
  );
}

function TokensContent({ namespace }: { namespace: string }) {
  const { t } = useTranslation('access');
  const { tenantId, can } = useAuth();
  const queryClient = useQueryClient();
  const key = useScopedQueryKey();
  const keepPrevious = useScopedPlaceholder();
  const now = useNow();
  const revokedSwitchId = useId();
  const canWrite = can(PERMISSIONS.tokenWrite);

  const [includeRevoked, setIncludeRevoked] = useState(false);
  const [createOpen, setCreateOpen] = useState(false);
  const [secret, setSecret] = useState<CreatedTokenSecret>();
  const [revokeTarget, setRevokeTarget] = useState<ApiToken>();
  const dialogOpen = createOpen || secret !== undefined || revokeTarget !== undefined;

  const pager = useCursorPagination({ resetOn: [tenantId, namespace, includeRevoked] });
  const query = useQuery({
    queryKey: key('access', 'tokens', { includeRevoked }, pager.pageToken, pager.pageSize),
    queryFn: ({ signal }) =>
      accessClient.listTokens(
        { namespace, includeRevoked, pageSize: pager.pageSize, pageToken: pager.pageToken },
        { signal },
      ),
    placeholderData: keepPrevious,
    refetchInterval: dialogOpen ? false : LIVE_REFETCH_MS,
  });

  const revoke = useMutation({
    mutationFn: (token: ApiToken) => accessClient.revokeToken({ id: token.id }),
    onSuccess: (_res, token) => {
      toast.success(t('tokens.revokedToast', { name: token.name }));
      void queryClient.invalidateQueries({ queryKey: ['access'] });
    },
  });

  const onRevoke = useCallback((token: ApiToken) => setRevokeTarget(token), []);
  const columns = useTokenColumns({ now, canWrite, onRevoke });

  const onCreated = (response: CreateTokenResponse) => {
    setCreateOpen(false);
    setSecret({
      name: response.token?.name ?? '',
      plaintext: response.plaintext,
    });
  };

  const createButton = (
    <PermissionButton permission={PERMISSIONS.tokenWrite} onClick={() => setCreateOpen(true)}>
      <PlusIcon />
      {t('tokens.create')}
    </PermissionButton>
  );

  return (
    <>
      <PageHeader
        title={t('tokens.title')}
        description={t('tokens.description', { namespace })}
        actions={
          <>
            <Button
              variant="outline"
              size="icon"
              onClick={() => void query.refetch()}
              aria-label={t('common:actions.refresh')}
            >
              <RefreshCwIcon className={query.isFetching ? 'animate-spin' : undefined} />
            </Button>
            {createButton}
          </>
        }
      />
      <PageIntro
        page="tokens"
        links={[
          { to: '/access/users', labelKey: 'nav.users' },
          { to: '/admin/tenants', labelKey: 'nav.tenants' },
        ]}
      />
      <DataTable
        columns={columns}
        data={query.data?.tokens}
        getRowId={(token) => token.id}
        isLoading={query.isLoading}
        isFetching={query.isFetching}
        error={query.error}
        onRetry={() => void query.refetch()}
        emptyTitle={t('tokens.empty')}
        emptyDescription={t('tokens.emptyDescription')}
        emptyAction={canWrite ? createButton : undefined}
        rowClassName={(token) => (token.revokedAt ? 'opacity-60' : undefined)}
        initialColumnVisibility={{ createdAt: false }}
        pagination={{ pager, nextPageToken: query.data?.nextPageToken, total: query.data?.total }}
        toolbar={
          <div className="flex items-center gap-2">
            <Switch id={revokedSwitchId} checked={includeRevoked} onCheckedChange={setIncludeRevoked} />
            <Label htmlFor={revokedSwitchId} className="text-sm font-normal">
              {t('tokens.includeRevoked')}
            </Label>
          </div>
        }
      />

      <CreateTokenDialog
        open={createOpen}
        onOpenChange={setCreateOpen}
        namespace={namespace}
        onCreated={onCreated}
      />
      <TokenSecretDialog secret={secret} onClose={() => setSecret(undefined)} />
      <ConfirmDialog
        open={revokeTarget !== undefined}
        onOpenChange={(open) => {
          if (!open) setRevokeTarget(undefined);
        }}
        destructive
        title={t('tokens.revokeTitle', { name: revokeTarget?.name ?? '' })}
        description={t('tokens.revokeDescription')}
        confirmText={revokeTarget?.name}
        confirmLabel={t('tokens.revoke')}
        onConfirm={() => (revokeTarget ? revoke.mutateAsync(revokeTarget) : undefined)}
      >
        {revokeTarget && (
          <div className="grid gap-1 rounded-md border bg-muted/30 px-3 py-2 text-sm">
            <span className="font-mono text-xs">{revokeTarget.tokenPrefix}…</span>
            <ScopeChips scopes={revokeTarget.scopes} max={8} />
          </div>
        )}
      </ConfirmDialog>
    </>
  );
}
