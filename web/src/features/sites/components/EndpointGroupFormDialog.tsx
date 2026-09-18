import { LoaderCircleIcon } from 'lucide-react';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

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
import { Textarea } from '@/components/ui/textarea';
import { type EndpointGroup, type Site } from '@/gen/spinneret/v1/site_admin_pb';
import { describeError, errorMessage } from '@/lib/errors';

import {
  buildUpdateGroupRequest,
  groupToForm,
  hasIssues,
  SITE_LIMITS,
  validateGroupForm,
  type GroupForm,
} from '../siteForm';
import { useCreateEndpointGroup, useUpdateEndpointGroup } from '../useSites';

export interface EndpointGroupFormDialogProps {
  site: Site;
  /** Client preselected for a new group. */
  client: string;
  /** Group to edit; create mode when undefined. */
  group?: EndpointGroup;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Called with the created group (e.g. to open its rule editor). */
  onCreated?: (group: EndpointGroup) => void;
}

/** Create or edit an endpoint group; client and name are immutable after creation. */
export function EndpointGroupFormDialog({
  site,
  client,
  group,
  open,
  onOpenChange,
  onCreated,
}: EndpointGroupFormDialogProps) {
  const { t } = useTranslation('sites');
  const mode = group ? 'edit' : 'create';
  const [form, setForm] = useState<GroupForm>(() =>
    group ? groupToForm(group) : { client, name: '', description: '', lowWatermark: '0' },
  );
  const [submitted, setSubmitted] = useState(false);
  const createMutation = useCreateEndpointGroup();
  const updateMutation = useUpdateEndpointGroup();
  const mutation = group ? updateMutation : createMutation;

  const issues = validateGroupForm(form, mode);
  const show = submitted ? issues : {};
  const update = (patch: Partial<GroupForm>) => setForm((prev) => ({ ...prev, ...patch }));

  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setSubmitted(true);
    if (hasIssues(issues)) return;
    try {
      if (group) {
        const request = buildUpdateGroupRequest(group, form);
        if (request.description === undefined && request.lowWatermark === undefined) {
          toast.info(t('form.noChanges'));
          onOpenChange(false);
          return;
        }
        await updateMutation.mutateAsync([group, form]);
        toast.success(t('groups.updated', { name: group.name }));
      } else {
        const res = await createMutation.mutateAsync({ site: site.name, form });
        toast.success(t('groups.created', { name: form.name }));
        if (res.endpointGroup) onCreated?.(res.endpointGroup);
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
          <DialogTitle>{group ? t('groups.editTitle') : t('groups.createTitle')}</DialogTitle>
          <DialogDescription>
            {group ? `${site.name} / ${group.client} / ${group.name}` : t('groups.createDescription')}
          </DialogDescription>
        </DialogHeader>
        <form className="grid gap-4" onSubmit={(e) => void submit(e)} noValidate>
          <div className="grid gap-4 sm:grid-cols-2">
            <FormField
              label={t('groups.client')}
              required={!group}
              error={show.client ? t('common:validation.required') : undefined}
            >
              <Select
                value={form.client}
                onValueChange={(v) => update({ client: v })}
                disabled={Boolean(group)}
              >
                <SelectTrigger className="w-full font-mono">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {site.clients.map((c) => (
                    <SelectItem key={c} value={c} className="font-mono">
                      {c}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </FormField>
            <FormField
              label={t('groups.name')}
              required={!group}
              description={group ? t('groups.nameImmutable') : t('form.nameHint')}
              error={show.name ? t(`groups.errors.name.${show.name}`) : undefined}
            >
              <Input
                value={form.name}
                onChange={(e) => update({ name: e.target.value })}
                disabled={Boolean(group)}
                className="font-mono"
                autoComplete="off"
                spellCheck={false}
                maxLength={64}
              />
            </FormField>
          </div>
          <FormField
            label={t('groups.description')}
            error={show.description ? t('form.errors.tooLong', { max: SITE_LIMITS.description }) : undefined}
          >
            <Textarea
              value={form.description}
              onChange={(e) => update({ description: e.target.value })}
              rows={2}
            />
          </FormField>
          <FormField
            label={t('groups.lowWatermark')}
            description={t('groups.lowWatermarkHint')}
            error={show.lowWatermark ? t('groups.errors.lowWatermark') : undefined}
          >
            <Input
              type="number"
              inputMode="numeric"
              min={0}
              value={form.lowWatermark}
              onChange={(e) => update({ lowWatermark: e.target.value })}
              className="w-40"
            />
          </FormField>
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
              {group ? t('common:actions.save') : t('common:actions.create')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
