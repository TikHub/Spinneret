import { PencilIcon, Trash2Icon } from 'lucide-react';
import { useCallback, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import { PERMISSIONS } from '@/app/auth/permissions';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { IdText } from '@/components/CopyButton';
import { StateBadge } from '@/components/StateBadge';
import { TimeAgo } from '@/components/TimeAgo';
import { Card, CardContent, CardHeader } from '@/components/ui/card';
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs';
import { type EndpointGroup, type Site } from '@/gen/spinneret/v1/site_admin_pb';
import { formatNumber } from '@/lib/format';

import { useSitePermissions } from '../useSitePermissions';
import { useDeleteEndpointGroup } from '../useSites';
import { EndpointGroupFormDialog } from './EndpointGroupFormDialog';
import { EndpointGroupsTable, type GroupAction } from './EndpointGroupsTable';
import { GuardedButton } from './GuardedButton';
import { SitePauseControl } from './SitePauseControl';
import { URIRulesSheet, type RuleEditorTarget } from './URIRulesSheet';
import { URITesterPanel } from './URITesterPanel';

type GroupDialog =
  | { type: 'create'; client: string }
  | { type: 'edit'; group: EndpointGroup }
  | { type: 'delete'; group: EndpointGroup }
  | { type: 'rules'; target: RuleEditorTarget };

function Meta({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid gap-0.5">
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="text-sm">{children}</dd>
    </div>
  );
}

export interface SiteDetailProps {
  site: Site;
  client: string | undefined;
  onClientChange: (client: string) => void;
  onEdit: () => void;
  onDelete: () => void;
  /** Pause auto refresh (a page-level dialog is open). */
  paused?: boolean;
}

/** Site header with switch and actions, endpoint groups per client, and the URI tester. */
export function SiteDetail({
  site,
  client,
  onClientChange,
  onEdit,
  onDelete,
  paused: pausedByPage = false,
}: SiteDetailProps) {
  const { t, i18n } = useTranslation('sites');
  const lng = i18n.language;
  const { canManageSites } = useSitePermissions();
  const [dialog, setDialog] = useState<GroupDialog | null>(null);
  const [pauseDialog, setPauseDialog] = useState(false);
  const deleteGroup = useDeleteEndpointGroup();
  const activeClient = client && site.clients.includes(client) ? client : (site.clients[0] ?? '');

  const onGroupAction = useCallback(
    (action: GroupAction, group: EndpointGroup) => {
      if (action === 'rules') {
        setDialog({
          type: 'rules',
          target: { id: group.id, name: group.name, client: group.client, site: site.name, siteId: site.id },
        });
      } else {
        setDialog({ type: action, group });
      }
    },
    [site.id, site.name],
  );
  const close = (type: GroupDialog['type']) => setDialog((cur) => (cur?.type === type ? null : cur));
  const paused = pausedByPage || dialog !== null || pauseDialog;

  return (
    <div className="grid min-w-0 content-start gap-4">
      <Card>
        <CardHeader className="flex flex-row flex-wrap items-start justify-between gap-3">
          <div className="grid min-w-0 gap-1">
            <div className="flex flex-wrap items-center gap-2">
              <h2 className="truncate text-lg font-semibold">{site.displayName || site.name}</h2>
              <StateBadge kind="site" state={site.paused ? 'paused' : 'active'} />
            </div>
            <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
              <span className="font-mono">{site.name}</span>
              <IdText value={site.id} />
            </div>
            {site.description && (
              <p className="max-w-3xl text-sm text-muted-foreground">{site.description}</p>
            )}
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <SitePauseControl site={site} onDialogChange={setPauseDialog} />
            <GuardedButton
              allowed={canManageSites}
              permission={PERMISSIONS.siteWrite}
              variant="outline"
              size="sm"
              onClick={onEdit}
            >
              <PencilIcon />
              {t('common:actions.edit')}
            </GuardedButton>
            <GuardedButton
              allowed={canManageSites}
              permission={PERMISSIONS.siteWrite}
              variant="outline"
              size="sm"
              className="text-destructive hover:text-destructive"
              onClick={onDelete}
            >
              <Trash2Icon />
              {t('common:actions.delete')}
            </GuardedButton>
          </div>
        </CardHeader>
        <CardContent>
          <dl className="grid grid-cols-2 gap-3 sm:grid-cols-3 xl:grid-cols-5">
            <Meta label={t('detail.endpointGroups')}>
              <span className="tabular">{formatNumber(site.endpointGroupCount, undefined, lng)}</span>
            </Meta>
            <Meta label={t('detail.identities')}>
              <span className="tabular">{formatNumber(site.identityCount, undefined, lng)}</span>
            </Meta>
            <Meta label={t('detail.created')}>
              <TimeAgo value={site.createdAt} past />
            </Meta>
            <Meta label={t('detail.updated')}>
              <TimeAgo value={site.updatedAt} past />
            </Meta>
            {site.paused && (
              <Meta label={t('detail.pausedSince')}>
                <TimeAgo value={site.pausedAt} past />
                {site.pausedReason && (
                  <span className="block text-xs break-words text-muted-foreground">{site.pausedReason}</span>
                )}
              </Meta>
            )}
          </dl>
        </CardContent>
      </Card>

      <section className="grid gap-2" aria-label={t('detail.endpointGroups')}>
        <h3 className="text-sm font-semibold">{t('detail.endpointGroups')}</h3>
        <Tabs value={activeClient} onValueChange={onClientChange}>
          <TabsList aria-label={t('detail.clients')}>
            {site.clients.map((c) => (
              <TabsTrigger key={c} value={c} className="font-mono">
                {c}
              </TabsTrigger>
            ))}
          </TabsList>
          {site.clients.map((c) => (
            <TabsContent key={c} value={c}>
              <EndpointGroupsTable
                site={site}
                client={c}
                paused={paused}
                onAction={onGroupAction}
                onCreate={() => setDialog({ type: 'create', client: c })}
              />
            </TabsContent>
          ))}
        </Tabs>
      </section>

      <URITesterPanel
        key={site.id}
        site={site}
        defaultClient={activeClient}
        onOpenRules={(target) => setDialog({ type: 'rules', target })}
      />

      {dialog?.type === 'create' && (
        <EndpointGroupFormDialog
          site={site}
          client={dialog.client}
          open
          onOpenChange={(open) => !open && close('create')}
          onCreated={(group) =>
            setDialog({
              type: 'rules',
              target: {
                id: group.id,
                name: group.name,
                client: group.client,
                site: site.name,
                siteId: site.id,
              },
            })
          }
        />
      )}
      {dialog?.type === 'edit' && (
        <EndpointGroupFormDialog
          site={site}
          client={dialog.group.client}
          group={dialog.group}
          open
          onOpenChange={(open) => !open && close('edit')}
        />
      )}
      {dialog?.type === 'delete' && (
        <ConfirmDialog
          open
          onOpenChange={(open) => !open && close('delete')}
          destructive
          title={t('groups.deleteTitle', { name: dialog.group.name })}
          description={t('groups.deleteDescription')}
          confirmLabel={t('common:actions.delete')}
          confirmText={dialog.group.name}
          onConfirm={async () => {
            await deleteGroup.mutateAsync(dialog.group.id);
            toast.success(t('groups.deleted', { name: dialog.group.name }));
          }}
        />
      )}
      {dialog?.type === 'rules' && (
        <URIRulesSheet
          key={dialog.target.id}
          target={dialog.target}
          open
          onOpenChange={(open) => !open && close('rules')}
        />
      )}
    </div>
  );
}
