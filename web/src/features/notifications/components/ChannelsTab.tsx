import { useMutation, useQueryClient } from '@tanstack/react-query';
import { BellIcon, PlusIcon, RefreshCwIcon } from 'lucide-react';
import { useCallback, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import { useAuth, useScopedQueryKey } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { DataTable, useCursorPagination } from '@/components/data-table';
import { Button } from '@/components/ui/button';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { SimpleTooltip } from '@/components/ui/tooltip';
import { type Channel, type ListChannelsResponse } from '@/gen/spinneret/v1/notification_admin_pb';
import { notificationClient } from '@/lib/clients';
import { errorMessage } from '@/lib/errors';

import { toggleChannelRequest } from '../channelForm';
import { useChannels, type ListScope } from '../useNotificationsApi';
import { ChannelFormDialog } from './ChannelFormDialog';
import { useChannelColumns } from './useChannelColumns';

/** Form dialog state; the channel is kept while the dialog animates closed. */
interface FormState {
  open: boolean;
  channel?: Channel;
}

/** Notification channels: list, enable switch, test delivery, create/edit/delete. */
export function ChannelsTab() {
  const { t } = useTranslation('notifications');
  const { tenantId, namespaceName, can, canInTenant } = useAuth();
  const queryClient = useQueryClient();
  const key = useScopedQueryKey();
  const [scope, setScope] = useState<ListScope>('all');
  const [form, setForm] = useState<FormState>({ open: false });
  const [deleteTarget, setDeleteTarget] = useState<Channel>();
  const [testing, setTesting] = useState<ReadonlySet<string>>(new Set());
  const dialogOpen = form.open || deleteTarget !== undefined;
  const pager = useCursorPagination({ resetOn: [tenantId, namespaceName, scope] });
  const channels = useChannels(scope, pager, dialogOpen);
  const canCreate = can(PERMISSIONS.notifyWrite) || canInTenant(PERMISSIONS.notifyWrite);

  const toggle = useMutation({
    mutationFn: ({ channel, enabled }: { channel: Channel; enabled: boolean }) =>
      notificationClient.updateChannel(toggleChannelRequest(channel, enabled)),
    // Optimistic: flip the switch in every cached channel page, roll back on failure.
    onMutate: async ({ channel, enabled }) => {
      const listKey = key('notifications', 'channels');
      await queryClient.cancelQueries({ queryKey: listKey });
      const previous = queryClient.getQueriesData<ListChannelsResponse>({ queryKey: listKey });
      queryClient.setQueriesData<ListChannelsResponse>({ queryKey: listKey }, (data) =>
        data
          ? { ...data, channels: data.channels.map((c) => (c.id === channel.id ? { ...c, enabled } : c)) }
          : data,
      );
      return { previous };
    },
    onError: (err, _vars, context) => {
      context?.previous.forEach(([queryKey, data]) => queryClient.setQueryData(queryKey, data));
      toast.error(errorMessage(err, t));
    },
    onSuccess: (res, { enabled }) =>
      toast.success(
        enabled
          ? t('channels.enabled', { name: res.channel?.name })
          : t('channels.disabled', { name: res.channel?.name }),
      ),
    onSettled: () => void queryClient.invalidateQueries({ queryKey: ['notifications'] }),
  });

  const test = useCallback(
    async (channel: Channel) => {
      setTesting((prev) => new Set(prev).add(channel.id));
      try {
        const res = await notificationClient.testChannel({ id: channel.id });
        const delivery = res.delivery;
        if (delivery?.ok) toast.success(t('channels.testOk', { name: channel.name }));
        else toast.error(t('channels.testFailed', { name: channel.name, error: delivery?.error || '—' }));
      } catch (err) {
        toast.error(errorMessage(err, t));
      } finally {
        setTesting((prev) => {
          const next = new Set(prev);
          next.delete(channel.id);
          return next;
        });
        void queryClient.invalidateQueries({ queryKey: ['notifications'] });
      }
    },
    [queryClient, t],
  );

  const toggleMutate = toggle.mutate;
  const columns = useChannelColumns({
    onToggle: useCallback(
      (channel: Channel, enabled: boolean) => toggleMutate({ channel, enabled }),
      [toggleMutate],
    ),
    onTest: useCallback((channel: Channel) => void test(channel), [test]),
    onEdit: useCallback((channel: Channel) => setForm({ open: true, channel }), []),
    onDelete: useCallback((channel: Channel) => setDeleteTarget(channel), []),
    testing,
  });

  const deleteChannel = async () => {
    if (!deleteTarget) return;
    await notificationClient.deleteChannel({ id: deleteTarget.id });
    toast.success(t('channels.deleted', { name: deleteTarget.name }));
    await queryClient.invalidateQueries({ queryKey: ['notifications'] });
  };

  const createButton = (
    <SimpleTooltip
      content={t('common:permission.missing', { permission: PERMISSIONS.notifyWrite })}
      enabled={!canCreate}
    >
      <span tabIndex={canCreate ? undefined : 0} className="inline-flex">
        <Button size="sm" disabled={!canCreate} onClick={() => setForm({ open: true })}>
          <PlusIcon />
          {t('channels.create')}
        </Button>
      </span>
    </SimpleTooltip>
  );

  return (
    <>
      <DataTable
        columns={columns}
        data={channels.data?.channels}
        getRowId={(c) => c.id}
        isLoading={channels.isLoading}
        isFetching={channels.isFetching}
        error={channels.error}
        onRetry={() => void channels.refetch()}
        emptyTitle={t('channels.empty')}
        emptyDescription={t('channels.emptyDescription')}
        emptyAction={createButton}
        initialColumnVisibility={{ sites: false }}
        toolbar={
          <div className="flex w-full flex-wrap items-center gap-2">
            <Select value={scope} onValueChange={(v) => setScope(v as ListScope)}>
              <SelectTrigger size="sm" className="w-56" aria-label={t('fields.scope')}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">{t('scope.all')}</SelectItem>
                <SelectItem value="namespace" disabled={!namespaceName}>
                  {t('scope.namespaceNamed', { namespace: namespaceName ?? '' })}
                </SelectItem>
              </SelectContent>
            </Select>
            <span className="hidden text-xs text-muted-foreground md:inline">
              <BellIcon className="mr-1 inline size-3.5" aria-hidden />
              {t('channels.autoRefresh')}
            </span>
            <div className="ml-auto flex items-center gap-2">
              <Button
                variant="outline"
                size="icon-sm"
                aria-label={t('common:actions.refresh')}
                onClick={() => void channels.refetch()}
              >
                <RefreshCwIcon className={channels.isFetching ? 'animate-spin' : undefined} />
              </Button>
              {createButton}
            </div>
          </div>
        }
        pagination={{ pager, nextPageToken: channels.data?.nextPageToken, total: channels.data?.total }}
      />
      <ChannelFormDialog
        open={form.open}
        onOpenChange={(open) => !open && setForm((prev) => ({ ...prev, open: false }))}
        channel={form.channel}
      />
      <ConfirmDialog
        open={deleteTarget !== undefined}
        onOpenChange={(open) => !open && setDeleteTarget(undefined)}
        title={t('channels.deleteTitle', { name: deleteTarget?.name ?? '' })}
        description={t('channels.deleteDescription')}
        confirmLabel={t('common:actions.delete')}
        destructive
        confirmText={deleteTarget?.name}
        onConfirm={deleteChannel}
      />
    </>
  );
}
