import { useMutation } from '@tanstack/react-query';
import { LoaderCircleIcon } from 'lucide-react';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import { useAuth } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';
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
import { FormField, FormStack } from '@/components/ui/form';
import { Input } from '@/components/ui/input';
import { Textarea } from '@/components/ui/textarea';
import { SiteSelect } from '@/features/identities/components/SiteSelect';
import { useInvalidateIdentityData } from '@/features/identities/notify';
import { hasFormErrors } from '@/features/identities/operations';
import { type Account } from '@/gen/spinneret/v1/identity_admin_pb';
import { identityClient } from '@/lib/clients';
import { errorMessage } from '@/lib/errors';

import {
  ACCOUNT_LIMITS,
  buildUpsertAccountRequest,
  upsertFormFromAccount,
  validateUpsertAccount,
  type UpsertAccountForm,
} from '../accounts';

export interface AccountUpsertDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Account to edit; creates (or updates by site + external reference) when omitted. */
  account?: Account;
  defaultSite?: string;
}

/** Creates or updates an account keyed by site and external reference (UpsertAccount). */
export function AccountUpsertDialog({
  open,
  onOpenChange,
  account,
  defaultSite = '',
}: AccountUpsertDialogProps) {
  const { t } = useTranslation('accounts');
  const { namespaceName, can } = useAuth();
  const invalidate = useInvalidateIdentityData();
  const editing = account !== undefined;
  const [form, setForm] = useState<UpsertAccountForm>(() => upsertFormFromAccount(account, defaultSite));
  const [touched, setTouched] = useState(false);
  const errors = validateUpsertAccount(form);
  const canWrite = can(PERMISSIONS.identityWrite, form.site || undefined);

  const mutation = useMutation({
    mutationFn: () => identityClient.upsertAccount(buildUpsertAccountRequest(namespaceName ?? '', form)),
    onSuccess: (res) => {
      toast.success(t('upsert.saved', { ref: res.account?.externalRef ?? form.externalRef }));
      invalidate();
      onOpenChange(false);
    },
    onError: (err) => toast.error(errorMessage(err, t)),
  });

  const submit = (event: FormEvent) => {
    event.preventDefault();
    setTouched(true);
    if (hasFormErrors(errors) || !canWrite) return;
    mutation.mutate();
  };

  const show = (error: string | undefined) => (touched ? error : undefined);

  return (
    <Dialog open={open} onOpenChange={(next) => !mutation.isPending && onOpenChange(next)}>
      <DialogContent className="sm:max-w-lg">
        <form onSubmit={submit} className="grid gap-4" noValidate>
          <DialogHeader>
            <DialogTitle>{editing ? t('upsert.editTitle') : t('upsert.createTitle')}</DialogTitle>
            <DialogDescription>{t('upsert.description')}</DialogDescription>
          </DialogHeader>
          <FormStack className="gap-3">
            <FormField
              label={t('fields.site')}
              required
              error={show(errors.site ? t('common:validation.required') : undefined)}
            >
              <SiteSelect
                value={form.site}
                onChange={(site) => setForm({ ...form, site })}
                disabled={editing || mutation.isPending}
              />
            </FormField>
            <FormField
              label={t('fields.externalRef')}
              required
              description={t('upsert.externalRefHint')}
              error={show(
                errors.externalRef === 'required'
                  ? t('common:validation.required')
                  : errors.externalRef
                    ? t('upsert.tooLong', { max: ACCOUNT_LIMITS.externalRef })
                    : undefined,
              )}
            >
              <Input
                value={form.externalRef}
                className="font-mono"
                disabled={editing || mutation.isPending}
                onChange={(e) => setForm({ ...form, externalRef: e.target.value })}
              />
            </FormField>
            <FormField
              label={t('fields.region')}
              error={errors.region ? t('upsert.tooLong', { max: ACCOUNT_LIMITS.region }) : undefined}
            >
              <Input value={form.region} onChange={(e) => setForm({ ...form, region: e.target.value })} />
            </FormField>
            <FormField label={t('fields.tags')} description={t('upsert.tagsHint')}>
              <TagsInput
                value={form.tags}
                onChange={(tags) => setForm({ ...form, tags })}
                maxTags={ACCOUNT_LIMITS.tags}
                validate={(tag) => tag.length <= ACCOUNT_LIMITS.tagLength}
              />
            </FormField>
            <FormField
              label={t('fields.notes')}
              error={errors.notes ? t('upsert.tooLong', { max: ACCOUNT_LIMITS.notes }) : undefined}
            >
              <Textarea
                rows={3}
                value={form.notes}
                onChange={(e) => setForm({ ...form, notes: e.target.value })}
              />
            </FormField>
          </FormStack>
          {!canWrite && form.site !== '' && (
            <p className="text-xs text-muted-foreground">
              {t('common:permission.missing', { permission: PERMISSIONS.identityWrite })}
            </p>
          )}
          <DialogFooter>
            <Button
              type="button"
              variant="ghost"
              onClick={() => onOpenChange(false)}
              disabled={mutation.isPending}
            >
              {t('common:actions.cancel')}
            </Button>
            <Button
              type="submit"
              disabled={mutation.isPending || !canWrite || (touched && hasFormErrors(errors))}
            >
              {mutation.isPending && <LoaderCircleIcon className="animate-spin" />}
              {t('common:actions.save')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
