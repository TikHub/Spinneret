import { LoaderCircleIcon, TriangleAlertIcon } from 'lucide-react';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import { TagsInput } from '@/components/TagsInput';
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
import { Textarea } from '@/components/ui/textarea';
import { type Site } from '@/gen/spinneret/v1/site_admin_pb';
import { describeError, errorMessage } from '@/lib/errors';

import {
  buildUpdateSiteRequest,
  DEFAULT_CLIENTS,
  EMPTY_SITE_FORM,
  hasIssues,
  isValidClient,
  removedClients,
  SITE_LIMITS,
  siteToForm,
  validateSiteForm,
  type SiteForm,
} from '../siteForm';
import { useCreateSite, useUpdateSite } from '../useSites';

export interface SiteFormDialogProps {
  /** Site to edit; create mode when undefined. */
  site?: Site;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onSaved: (site: Site) => void;
}

/** Create or edit a site. The name is immutable after creation. */
export function SiteFormDialog({ site, open, onOpenChange, onSaved }: SiteFormDialogProps) {
  const { t } = useTranslation('sites');
  const mode = site ? 'edit' : 'create';
  const [form, setForm] = useState<SiteForm>(() => (site ? siteToForm(site) : EMPTY_SITE_FORM));
  const [submitted, setSubmitted] = useState(false);
  const createMutation = useCreateSite();
  const updateMutation = useUpdateSite();
  const mutation = site ? updateMutation : createMutation;

  const issues = validateSiteForm(form, mode);
  const show = submitted ? issues : {};
  const update = (patch: Partial<SiteForm>) => setForm((prev) => ({ ...prev, ...patch }));
  const removed = site ? removedClients(site, form) : [];

  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setSubmitted(true);
    if (hasIssues(issues)) return;
    try {
      if (site) {
        const request = buildUpdateSiteRequest(site, form);
        if (
          request.displayName === undefined &&
          request.description === undefined &&
          request.clients.length === 0
        ) {
          toast.info(t('form.noChanges'));
          onOpenChange(false);
          return;
        }
        const res = await updateMutation.mutateAsync([site, form]);
        toast.success(t('form.updated', { name: site.name }));
        if (res.site) onSaved(res.site);
      } else {
        const res = await createMutation.mutateAsync(form);
        toast.success(t('form.created', { name: form.name }));
        if (res.site) onSaved(res.site);
      }
      onOpenChange(false);
    } catch (err) {
      toast.error(errorMessage(err, t));
    }
  };

  const apiError = mutation.error ? describeError(mutation.error, t) : undefined;

  return (
    <Dialog open={open} onOpenChange={(next) => !mutation.isPending && onOpenChange(next)}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{site ? t('form.editTitle') : t('form.createTitle')}</DialogTitle>
          <DialogDescription>{site ? site.name : t('form.createDescription')}</DialogDescription>
        </DialogHeader>
        <form className="grid gap-4" onSubmit={(e) => void submit(e)} noValidate>
          <FormField
            label={t('form.name')}
            required={!site}
            description={site ? t('form.nameImmutable') : t('form.nameHint')}
            error={show.name ? t(`form.errors.name.${show.name}`) : undefined}
          >
            <Input
              value={form.name}
              onChange={(e) => update({ name: e.target.value })}
              readOnly={Boolean(site)}
              disabled={Boolean(site)}
              className="font-mono"
              autoComplete="off"
              spellCheck={false}
              maxLength={64}
            />
          </FormField>
          <FormField
            label={t('form.displayName')}
            error={show.displayName ? t('form.errors.tooLong', { max: SITE_LIMITS.displayName }) : undefined}
          >
            <Input value={form.displayName} onChange={(e) => update({ displayName: e.target.value })} />
          </FormField>
          <FormField
            label={t('form.description')}
            error={show.description ? t('form.errors.tooLong', { max: SITE_LIMITS.description }) : undefined}
          >
            <Textarea
              value={form.description}
              onChange={(e) => update({ description: e.target.value })}
              rows={3}
            />
          </FormField>
          <FormField
            label={t('form.clients')}
            required={Boolean(site)}
            description={
              site
                ? t('form.clientsEditHint')
                : t('form.clientsHint', { defaults: DEFAULT_CLIENTS.join(', ') })
            }
            error={
              show.clients
                ? t(`form.errors.clients.${show.clients}`, { max: SITE_LIMITS.clients })
                : undefined
            }
          >
            <TagsInput
              value={form.clients}
              onChange={(clients) => update({ clients })}
              validate={isValidClient}
              maxTags={SITE_LIMITS.clients}
              placeholder={t('form.clientsPlaceholder')}
            />
          </FormField>
          {removed.length > 0 && (
            <p className="flex gap-2 rounded-md border border-amber-500/30 bg-amber-500/5 px-3 py-2 text-sm text-amber-700 dark:text-amber-400">
              <TriangleAlertIcon className="mt-0.5 size-4 shrink-0" aria-hidden />
              {t('form.clientsRemoved', { clients: removed.join(', ') })}
            </p>
          )}
          {apiError && (
            <p role="alert" className="text-sm text-destructive">
              {apiError.title}
              {apiError.detail && apiError.detail !== apiError.title && `: ${apiError.detail}`}
            </p>
          )}
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => onOpenChange(false)}
              disabled={mutation.isPending}
            >
              {t('common:actions.cancel')}
            </Button>
            <Button type="submit" disabled={mutation.isPending}>
              {mutation.isPending && <LoaderCircleIcon className="animate-spin" />}
              {site ? t('common:actions.save') : t('common:actions.create')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
