import { KeyRoundIcon, LoaderCircleIcon } from 'lucide-react';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import { ConfirmDialog } from '@/components/ConfirmDialog';
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
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { Switch } from '@/components/ui/switch';
import { type Proxy } from '@/gen/spinneret/v1/proxy_admin_pb';
import { describeError, errorMessage } from '@/lib/errors';

import { PROXY_KINDS } from '../proxyFilters';
import {
  buildUpdateProxyRequest,
  hasIssues,
  isEmptyUpdate,
  PROXY_LIMITS,
  proxyToEditForm,
  validateProxyEditForm,
  type ProxyEditForm,
} from '../proxyForm';
import { useUpdateProxy } from '../useProxies';
import { SessionTemplateField } from './SessionTemplateField';

export interface EditProxyDialogProps {
  proxy: Proxy;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

/** Edits proxy attributes; replacing the URL (credentials) needs a repeated entry and a confirmation. */
export function EditProxyDialog({ proxy, open, onOpenChange }: EditProxyDialogProps) {
  const { t } = useTranslation('proxies');
  const [form, setForm] = useState<ProxyEditForm>(() => proxyToEditForm(proxy));
  const [submitted, setSubmitted] = useState(false);
  const [confirmUrl, setConfirmUrl] = useState(false);
  const mutation = useUpdateProxy();

  const issues = validateProxyEditForm(form);
  const show = submitted ? issues : {};
  const update = (patch: Partial<ProxyEditForm>) => setForm((prev) => ({ ...prev, ...patch }));
  const tooLong = (max: number) => t('edit.errors.tooLong', { max });
  const name = proxy.displayUrl || proxy.id;

  const save = async () => {
    const request = buildUpdateProxyRequest(proxy, form);
    await mutation.mutateAsync(request);
    toast.success(request.url ? t('edit.urlReplaced', { name }) : t('edit.saved', { name }));
    onOpenChange(false);
  };

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setSubmitted(true);
    if (hasIssues(issues)) return;
    if (isEmptyUpdate(buildUpdateProxyRequest(proxy, form))) {
      toast.info(t('edit.noChanges'));
      onOpenChange(false);
      return;
    }
    if (form.replaceUrl) {
      setConfirmUrl(true);
      return;
    }
    save().catch((err: unknown) => toast.error(errorMessage(err, t)));
  };

  const apiError = mutation.error ? describeError(mutation.error, t) : undefined;

  return (
    <>
      <Dialog open={open} onOpenChange={(next) => !mutation.isPending && onOpenChange(next)}>
        <DialogContent className="sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>{t('edit.title')}</DialogTitle>
            <DialogDescription className="font-mono text-xs break-all">{name}</DialogDescription>
          </DialogHeader>
          <form className="grid gap-4" onSubmit={submit} noValidate>
            <div className="grid gap-3 rounded-md border p-3">
              <FormField label={t('edit.replaceUrl')} inline>
                <Switch
                  checked={form.replaceUrl}
                  onCheckedChange={(replaceUrl) => update({ replaceUrl, url: '', urlConfirm: '' })}
                />
              </FormField>
              <p className="text-xs text-muted-foreground">{t('edit.replaceUrlHint')}</p>
              {form.replaceUrl && (
                <div className="grid gap-3 sm:grid-cols-2">
                  <FormField
                    label={t('edit.url')}
                    required
                    error={show.url ? t(`edit.errors.url.${show.url}`, { max: PROXY_LIMITS.url }) : undefined}
                  >
                    <Input
                      type="password"
                      autoComplete="new-password"
                      spellCheck={false}
                      value={form.url}
                      onChange={(e) => update({ url: e.target.value })}
                      placeholder={t('edit.urlPlaceholder')}
                      className="font-mono"
                    />
                  </FormField>
                  <FormField
                    label={t('edit.urlConfirm')}
                    required
                    error={show.urlConfirm ? t('edit.errors.urlConfirm') : undefined}
                  >
                    <Input
                      type="password"
                      autoComplete="new-password"
                      spellCheck={false}
                      value={form.urlConfirm}
                      onChange={(e) => update({ urlConfirm: e.target.value })}
                      className="font-mono"
                    />
                  </FormField>
                </div>
              )}
            </div>
            <div className="grid gap-3 sm:grid-cols-2">
              <FormField label={t('edit.kind')} error={show.kind ? t('edit.errors.kind') : undefined}>
                <Select value={form.kind} onValueChange={(kind) => update({ kind })}>
                  <SelectTrigger className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {PROXY_KINDS.map((kind) => (
                      <SelectItem key={kind} value={kind}>
                        {t(`kinds.${kind}`)}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </FormField>
              <FormField
                label={t('edit.maxConcurrency')}
                error={
                  show.maxConcurrency
                    ? t('edit.errors.maxConcurrency', { max: PROXY_LIMITS.maxConcurrency })
                    : undefined
                }
              >
                <Input
                  type="number"
                  inputMode="numeric"
                  min={1}
                  max={PROXY_LIMITS.maxConcurrency}
                  value={form.maxConcurrency}
                  onChange={(e) => update({ maxConcurrency: e.target.value })}
                />
              </FormField>
              <FormField
                label={t('edit.region')}
                error={show.region ? tooLong(PROXY_LIMITS.region) : undefined}
              >
                <Input value={form.region} onChange={(e) => update({ region: e.target.value })} />
              </FormField>
              <FormField label={t('edit.city')} error={show.city ? tooLong(PROXY_LIMITS.city) : undefined}>
                <Input value={form.city} onChange={(e) => update({ city: e.target.value })} />
              </FormField>
              <FormField
                label={t('edit.provider')}
                error={show.provider ? tooLong(PROXY_LIMITS.provider) : undefined}
                className="sm:col-span-2"
              >
                <Input value={form.provider} onChange={(e) => update({ provider: e.target.value })} />
              </FormField>
              <FormField label={t('edit.tags')} className="sm:col-span-2">
                <TagsInput
                  value={form.tags}
                  onChange={(tags) => update({ tags })}
                  maxTags={PROXY_LIMITS.tags}
                  validate={(tag) => tag.length <= PROXY_LIMITS.tag}
                />
              </FormField>
            </div>
            <SessionTemplateField
              value={form.sessionTemplate}
              onChange={(sessionTemplate) => update({ sessionTemplate })}
            />
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
                {t('common:actions.save')}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
      <ConfirmDialog
        open={confirmUrl}
        onOpenChange={setConfirmUrl}
        destructive
        title={
          <span className="inline-flex items-center gap-2">
            <KeyRoundIcon className="size-4" aria-hidden />
            {t('edit.confirmUrlTitle')}
          </span>
        }
        description={t('edit.confirmUrlDescription', { name })}
        confirmLabel={t('edit.confirmUrlAction')}
        onConfirm={save}
      />
    </>
  );
}
