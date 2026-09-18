import { useMutation, useQueryClient } from '@tanstack/react-query';
import { LoaderCircleIcon } from 'lucide-react';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import { useAuth } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';
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
import { Input } from '@/components/ui/input';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { Separator } from '@/components/ui/separator';
import { Switch } from '@/components/ui/switch';
import { type Channel } from '@/gen/spinneret/v1/notification_admin_pb';
import { notificationClient } from '@/lib/clients';
import { errorMessage } from '@/lib/errors';
import { sensitiveMutation } from '@/lib/sensitiveMutation';

import {
  CHANNEL_KINDS,
  configToForm,
  EMPTY_CHANNEL_CONFIG,
  isChannelKind,
  MAX_CHANNEL_NAME_LENGTH,
  SEVERITIES,
  type Severity,
} from '../channelConfig';
import {
  createChannelRequest,
  formFromChannel,
  hasChannelErrors,
  newChannelForm,
  updateChannelRequest,
  validateChannelForm,
  type ChannelFormValues,
  type ChannelScope,
} from '../channelForm';
import { ChannelKindLabel } from './badges';
import { ChannelConfigFields } from './ChannelConfigFields';
import { EventTypesPicker, SitesPicker } from './ChannelPickers';

export interface ChannelFormDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Channel to edit; create mode when undefined. */
  channel?: Channel;
}

interface ChannelFormProps {
  channel?: Channel;
  onOpenChange: (open: boolean) => void;
  onPendingChange: (pending: boolean) => void;
}

/** Form body, mounted only while the dialog is open (typed secrets are dropped on close). */
function ChannelForm({ channel, onOpenChange, onPendingChange }: ChannelFormProps) {
  const { t } = useTranslation('notifications');
  const { namespaceName, can, canInTenant } = useAuth();
  const queryClient = useQueryClient();
  const canTenant = canInTenant(PERMISSIONS.notifyWrite);
  const canNamespace = can(PERMISSIONS.notifyWrite);
  const [values, setValues] = useState<ChannelFormValues>(() =>
    channel
      ? formFromChannel(channel)
      : newChannelForm(
          namespaceName ?? '',
          namespaceName && (canNamespace || !canTenant) ? 'namespace' : 'tenant',
        ),
  );
  const [touched, setTouched] = useState(false);
  const initialConfig = channel ? configToForm(channel.config) : EMPTY_CHANNEL_CONFIG;
  const errors = validateChannelForm(values, channel);
  const set = <K extends keyof ChannelFormValues>(field: K, value: ChannelFormValues[K]) =>
    setValues((prev) => ({ ...prev, [field]: value }));

  // The variables may hold new credentials (secrets, bot tokens, header values): drop the mutation from the
  // cache as soon as the form unmounts (the default keeps it for 5 minutes).
  const mutation = useMutation(
    sensitiveMutation({
      mutationFn: async (form: ChannelFormValues) => {
        if (channel)
          return (await notificationClient.updateChannel(updateChannelRequest(channel, form))).channel;
        return (await notificationClient.createChannel(createChannelRequest(form))).channel;
      },
      onMutate: () => onPendingChange(true),
      onSettled: () => onPendingChange(false),
      onSuccess: (saved) => {
        toast.success(
          channel ? t('form.updated', { name: saved?.name }) : t('form.created', { name: saved?.name }),
        );
        void queryClient.invalidateQueries({ queryKey: ['notifications'] });
        onOpenChange(false);
      },
      onError: (err) => toast.error(errorMessage(err, t)),
    }),
  );

  const submit = (event: FormEvent) => {
    event.preventDefault();
    setTouched(true);
    if (!hasChannelErrors(errors)) mutation.mutate(values);
  };

  const scopeLabel = (scope: ChannelScope) =>
    scope === 'tenant' ? t('scope.tenant') : t('scope.namespaceNamed', { namespace: values.namespace });

  return (
    <form onSubmit={submit} className="grid gap-4" noValidate autoComplete="off">
      <DialogHeader>
        <DialogTitle>
          {channel ? t('form.editTitle', { name: channel.name }) : t('form.createTitle')}
        </DialogTitle>
        <DialogDescription>{t('form.description')}</DialogDescription>
      </DialogHeader>
      <div className="grid gap-4 sm:grid-cols-2">
        <FormField
          label={t('fields.name')}
          required
          error={touched && errors.name ? t(`validation.name.${errors.name}`) : undefined}
        >
          <Input
            value={values.name}
            maxLength={MAX_CHANNEL_NAME_LENGTH}
            onChange={(e) => set('name', e.target.value)}
          />
        </FormField>
        <FormField label={t('fields.scope')} description={channel ? t('form.scopeFixed') : undefined}>
          <Select
            value={values.scope}
            disabled={Boolean(channel)}
            onValueChange={(v) => setValues((prev) => ({ ...prev, scope: v as ChannelScope, sites: [] }))}
          >
            <SelectTrigger className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="namespace" disabled={!channel && (!canNamespace || !namespaceName)}>
                {scopeLabel('namespace')}
              </SelectItem>
              <SelectItem value="tenant" disabled={!channel && !canTenant}>
                {scopeLabel('tenant')}
              </SelectItem>
            </SelectContent>
          </Select>
        </FormField>
        <FormField label={t('fields.kind')} description={channel ? t('form.kindFixed') : undefined}>
          <Select
            value={values.kind}
            disabled={Boolean(channel)}
            onValueChange={(v) => isChannelKind(v) && set('kind', v)}
          >
            <SelectTrigger className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {CHANNEL_KINDS.map((kind) => (
                <SelectItem key={kind} value={kind}>
                  <ChannelKindLabel kind={kind} />
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </FormField>
        <FormField label={t('fields.minSeverity')} description={t('form.minSeverityHint')}>
          <Select value={values.minSeverity} onValueChange={(v) => set('minSeverity', v as Severity)}>
            <SelectTrigger className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {SEVERITIES.map((severity) => (
                <SelectItem key={severity} value={severity}>
                  {t(`severities.${severity}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </FormField>
      </div>
      <Separator />
      <ChannelConfigFields
        kind={values.kind}
        value={values.config}
        onChange={(config) => set('config', config)}
        initial={initialConfig}
        errors={errors.config}
        showErrors={touched}
      />
      <Separator />
      <FormField
        label={t('fields.eventTypes')}
        required
        error={touched && errors.eventTypes ? t('validation.eventTypes') : undefined}
      >
        <EventTypesPicker value={values.eventTypes} onChange={(v) => set('eventTypes', v)} />
      </FormField>
      {values.scope === 'namespace' ? (
        <FormField label={t('fields.sites')} description={t('form.sitesHint')}>
          <SitesPicker namespace={values.namespace} value={values.sites} onChange={(v) => set('sites', v)} />
        </FormField>
      ) : (
        <p className="text-xs text-muted-foreground">{t('form.tenantSites')}</p>
      )}
      <FormField inline label={t('fields.enabled')}>
        <Switch checked={values.enabled} onCheckedChange={(v) => set('enabled', v)} />
      </FormField>
      <DialogFooter>
        <Button
          type="button"
          variant="outline"
          disabled={mutation.isPending}
          onClick={() => onOpenChange(false)}
        >
          {t('common:actions.cancel')}
        </Button>
        <Button type="submit" disabled={mutation.isPending}>
          {mutation.isPending && <LoaderCircleIcon className="animate-spin" />}
          {channel ? t('common:actions.save') : t('common:actions.create')}
        </Button>
      </DialogFooter>
    </form>
  );
}

/** Create or edit a notification channel with kind-specific settings. */
export function ChannelFormDialog({ open, onOpenChange, channel }: ChannelFormDialogProps) {
  const [pending, setPending] = useState(false);
  return (
    <Dialog open={open} onOpenChange={(next) => !pending && onOpenChange(next)}>
      <DialogContent className="sm:max-w-2xl">
        <ChannelForm
          key={channel?.id ?? 'new'}
          channel={channel}
          onOpenChange={onOpenChange}
          onPendingChange={setPending}
        />
      </DialogContent>
    </Dialog>
  );
}
