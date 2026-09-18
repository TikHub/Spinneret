import { Code } from '@connectrpc/connect';
import { useMutation } from '@tanstack/react-query';
import { LoaderCircleIcon } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import { useAuth } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';
import { PermissionButton } from '@/app/auth/PermissionGate';
import { Button } from '@/components/ui/button';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { FormField } from '@/components/ui/form';
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs';
import { SiteSelect } from '@/features/identities/components/SiteSelect';
import { useInvalidateIdentityData } from '@/features/identities/notify';
import { type IdentityType, type PreviewDeliveryResponse } from '@/gen/spinneret/v1/identity_admin_pb';
import { identityClient } from '@/lib/clients';
import { errorMessage, hasCode, toApiError } from '@/lib/errors';

import { initialPreviewPayload } from '../credential';
import { EMPTY_SPEC } from '../examples';
import { DeliveryPreviewPanel, type PreviewSource } from './DeliveryPreviewPanel';
import { SpecEditorPanel } from './SpecEditorPanel';

export type EditorTab = 'spec' | 'preview';

export interface IdentityTypeEditorDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Existing type to edit; a new type is created when omitted. */
  identityType?: IdentityType;
  defaultSite?: string;
  initialTab?: EditorTab;
}

/** Create or edit an identity type from its YAML spec, with a delivery preview tab. */
export function IdentityTypeEditorDialog({
  open,
  onOpenChange,
  identityType,
  defaultSite = '',
  initialTab = 'spec',
}: IdentityTypeEditorDialogProps) {
  const { t } = useTranslation('identity-types');
  const { namespaceName, can } = useAuth();
  const invalidate = useInvalidateIdentityData();
  const editing = identityType !== undefined;
  const [site, setSite] = useState(identityType?.site ?? defaultSite);
  const [yaml, setYaml] = useState(identityType?.specYaml ?? EMPTY_SPEC);
  const [tab, setTab] = useState<EditorTab>(initialTab);
  const [serverErrors, setServerErrors] = useState<string[]>([]);
  const [previewPayload, setPreviewPayload] = useState(() => initialPreviewPayload(identityType?.fields));
  const [previewResult, setPreviewResult] = useState<PreviewDeliveryResponse>();
  const canWrite = can(PERMISSIONS.siteWrite, site || undefined);
  const dirty = !editing || yaml !== identityType.specYaml;

  const save = useMutation({
    mutationFn: async () => {
      if (identityType) {
        return (await identityClient.updateIdentityType({ id: identityType.id, specYaml: yaml }))
          .identityType;
      }
      return (
        await identityClient.createIdentityType({ namespace: namespaceName ?? '', site, specYaml: yaml })
      ).identityType;
    },
    onSuccess: (saved) => {
      setServerErrors([]);
      toast.success(
        editing
          ? t('editor.updated', { name: saved?.name ?? '', version: saved?.version ?? 0 })
          : t('editor.created', { name: saved?.name ?? '' }),
      );
      invalidate();
      onOpenChange(false);
    },
    onError: (err) => {
      if (hasCode(err, Code.InvalidArgument, Code.FailedPrecondition, Code.AlreadyExists)) {
        setServerErrors([toApiError(err).message].filter(Boolean));
        setTab('spec');
      }
      toast.error(errorMessage(err, t));
    },
  });

  const source: PreviewSource =
    editing && !dirty ? { case: 'typeId', value: identityType.id } : { case: 'specYaml', value: yaml };

  return (
    <Dialog open={open} onOpenChange={(next) => !save.isPending && onOpenChange(next)}>
      <DialogContent className="sm:max-w-5xl">
        <DialogHeader>
          <DialogTitle>
            {editing ? t('editor.editTitle', { name: identityType.name }) : t('editor.createTitle')}
          </DialogTitle>
          <DialogDescription>
            {editing
              ? t('editor.editDescription', { site: identityType.site, version: identityType.version })
              : t('editor.createDescription')}
          </DialogDescription>
        </DialogHeader>
        {!editing && (
          <FormField
            label={t('fields.site')}
            required
            description={t('editor.siteHint')}
            className="max-w-sm"
          >
            <SiteSelect value={site} onChange={setSite} disabled={save.isPending} />
          </FormField>
        )}
        {!canWrite && site !== '' && (
          <p className="text-xs text-muted-foreground">
            {t('common:permission.missing', { permission: 'site:write' })}
          </p>
        )}
        <Tabs value={tab} onValueChange={(v) => setTab(v as EditorTab)}>
          <TabsList>
            <TabsTrigger value="spec">{t('editor.tabSpec')}</TabsTrigger>
            <TabsTrigger value="preview" disabled={site === ''}>
              {t('editor.tabPreview')}
            </TabsTrigger>
          </TabsList>
          <TabsContent value="spec">
            <SpecEditorPanel
              value={yaml}
              onChange={setYaml}
              serverErrors={serverErrors}
              readOnly={!canWrite || save.isPending}
              editorPath={`identity-type-${identityType?.id ?? 'new'}.yaml`}
            />
          </TabsContent>
          <TabsContent value="preview">
            {site !== '' && (
              <DeliveryPreviewPanel
                site={site}
                source={source}
                payload={previewPayload}
                onPayloadChange={setPreviewPayload}
                result={previewResult}
                onResult={setPreviewResult}
                editorPath={`identity-type-${identityType?.id ?? 'new'}-preview.json`}
              />
            )}
          </TabsContent>
        </Tabs>
        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)} disabled={save.isPending}>
            {editing && !canWrite ? t('common:actions.close') : t('common:actions.cancel')}
          </Button>
          <PermissionButton
            permission={PERMISSIONS.siteWrite}
            site={site || undefined}
            disabled={save.isPending || site === '' || yaml.trim() === '' || !dirty || !namespaceName}
            onClick={() => save.mutate()}
          >
            {save.isPending && <LoaderCircleIcon className="animate-spin" />}
            {editing ? t('editor.save') : t('editor.create')}
          </PermissionButton>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
