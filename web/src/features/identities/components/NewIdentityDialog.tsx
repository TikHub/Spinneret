import { useMutation } from '@tanstack/react-query';
import { LoaderCircleIcon, PlusIcon } from 'lucide-react';
import { useState } from 'react';
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
import { Separator } from '@/components/ui/separator';
import { type IdentityType } from '@/gen/spinneret/v1/identity_admin_pb';
import { identityClient } from '@/lib/clients';
import { errorMessage } from '@/lib/errors';
import { sensitiveMutation } from '@/lib/sensitiveMutation';

import {
  buildNewIdentityRow,
  emptyFieldValues,
  type FieldError,
  type FieldValues,
  type NewIdentityMeta,
} from '../newIdentity';
import { useInvalidateIdentityData } from '../notify';
import { useIdentityTypeOptions } from '../useIdentityOptions';
import { IdentityFieldInput } from './IdentityFieldInput';
import { IdentityTypeSelect } from './IdentityTypeSelect';
import { SiteSelect } from './SiteSelect';

export interface NewIdentityDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  defaultSite?: string;
  defaultType?: string;
}

const EMPTY_META: NewIdentityMeta = { account: '', region: '', tags: '' };

/**
 * Creates a single identity from a form built out of its type's fields.
 *
 * The row is handed to ImportIdentities, the same path a bulk import takes, so
 * a hand-entered credential is normalized, deduplicated and activated by
 * exactly the rules a file import follows.
 */
export function NewIdentityDialog({
  open,
  onOpenChange,
  defaultSite = '',
  defaultType = '',
}: NewIdentityDialogProps) {
  const { t } = useTranslation('identities');
  const { namespaceName, can } = useAuth();
  const invalidate = useInvalidateIdentityData();

  const [site, setSite] = useState(defaultSite);
  const [type, setType] = useState(defaultType);
  const [values, setValues] = useState<FieldValues>({});
  const [meta, setMeta] = useState<NewIdentityMeta>(EMPTY_META);
  const [errors, setErrors] = useState<Record<string, FieldError>>({});
  const [formOfType, setFormOfType] = useState<string>();

  // Derived rather than held in state, so a type passed in as a default gets
  // its form as soon as the options load, without waiting for a manual pick.
  const typeOptions = useIdentityTypeOptions(site, site !== '');
  const identityType: IdentityType | undefined = (typeOptions.data ?? []).find((o) => o.name === type);
  const fields = identityType?.fields ?? [];

  // Reset the form when it comes to stand for a different type (the React
  // "adjust state during render" pattern; no effect round-trip).
  const formKey = identityType?.id;
  if (formKey !== formOfType) {
    setFormOfType(formKey);
    setValues(identityType ? emptyFieldValues(identityType.fields) : {});
    setErrors({});
  }

  const canWrite = can(PERMISSIONS.identityWrite, site || undefined);
  const ready = Boolean(namespaceName) && site !== '' && type !== '' && fields.length > 0;

  const selectSite = (name: string) => {
    setSite(name);
    setType('');
  };

  // The variables hold an identity payload (a credential).
  const create = useMutation(
    sensitiveMutation({
      mutationFn: (line: string) =>
        identityClient.importIdentities({
          namespace: namespaceName ?? '',
          site,
          type,
          format: 'jsonl',
          mode: 'upsert',
          data: line,
        }),
      onSuccess: (response) => {
        const failure = response.failed[0];
        if (failure) {
          // A single row that the server rejected: show why and keep the form
          // filled in so the value can be corrected rather than retyped.
          toast.error(t('newIdentity.rejected', { reason: failure.message }));
          return;
        }
        invalidate();
        if (response.updated > 0) toast.success(t('newIdentity.updated'));
        else if (response.unchanged > 0) toast.info(t('newIdentity.unchanged'));
        else toast.success(t('newIdentity.created'));
        onOpenChange(false);
      },
      onError: (err) => toast.error(errorMessage(err, t)),
    }),
  );

  const submit = () => {
    const row = buildNewIdentityRow(fields, values, meta);
    if (!row.ok) {
      setErrors(row.errors);
      return;
    }
    setErrors({});
    create.mutate(row.line);
  };

  return (
    <Dialog open={open} onOpenChange={(next) => !create.isPending && onOpenChange(next)}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{t('newIdentity.title')}</DialogTitle>
          <DialogDescription>{t('newIdentity.description')}</DialogDescription>
        </DialogHeader>

        <div className="grid max-h-[60vh] gap-4 overflow-y-auto px-1">
          <div className="grid gap-4 sm:grid-cols-2">
            <FormField label={t('fields.site')} required>
              <SiteSelect value={site} onChange={selectSite} disabled={create.isPending} />
            </FormField>
            <FormField label={t('fields.type')} required>
              <IdentityTypeSelect
                site={site}
                value={type}
                onChange={setType}
                disabled={site === '' || create.isPending}
              />
            </FormField>
          </div>

          {type !== '' && fields.length === 0 && (
            <p className="text-sm text-muted-foreground">{t('newIdentity.noFields')}</p>
          )}

          {fields.length > 0 && (
            <>
              <Separator />
              {fields.map((field) => (
                <IdentityFieldInput
                  key={field.name}
                  field={field}
                  value={values[field.name] ?? ''}
                  onChange={(next) => setValues((prev) => ({ ...prev, [field.name]: next }))}
                  error={errors[field.name]}
                  disabled={create.isPending}
                />
              ))}
              <Separator />
              <div className="grid gap-4 sm:grid-cols-3">
                <FormField label={t('newIdentity.account')} description={t('newIdentity.accountHint')}>
                  <Input
                    value={meta.account}
                    onChange={(e) => setMeta((prev) => ({ ...prev, account: e.target.value }))}
                    disabled={create.isPending}
                    spellCheck={false}
                  />
                </FormField>
                <FormField label={t('newIdentity.region')}>
                  <Input
                    value={meta.region}
                    onChange={(e) => setMeta((prev) => ({ ...prev, region: e.target.value }))}
                    disabled={create.isPending}
                    spellCheck={false}
                  />
                </FormField>
                <FormField label={t('newIdentity.tags')} description={t('newIdentity.tagsHint')}>
                  <Input
                    value={meta.tags}
                    onChange={(e) => setMeta((prev) => ({ ...prev, tags: e.target.value }))}
                    disabled={create.isPending}
                    spellCheck={false}
                  />
                </FormField>
              </div>
            </>
          )}
        </div>

        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)} disabled={create.isPending}>
            {t('common:actions.cancel')}
          </Button>
          <Button onClick={submit} disabled={!ready || !canWrite || create.isPending}>
            {create.isPending ? <LoaderCircleIcon className="animate-spin" /> : <PlusIcon />}
            {t('newIdentity.submit')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
