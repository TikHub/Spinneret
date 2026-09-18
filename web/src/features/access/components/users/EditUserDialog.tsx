import { useMutation, useQueryClient } from '@tanstack/react-query';
import { LoaderCircleIcon } from 'lucide-react';
import { useId, useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import { useAuth } from '@/app/auth/AuthContext';
import { ConfirmDialog } from '@/components/ConfirmDialog';
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
import { Label } from '@/components/ui/label';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { Switch } from '@/components/ui/switch';
import { type User } from '@/gen/spinneret/v1/auth_pb';
import { LANGUAGE_NAMES, SUPPORTED_LANGUAGES } from '@/i18n';
import { accessClient } from '@/lib/clients';
import { errorMessage } from '@/lib/errors';

import { hasErrors } from '../../forms';
import {
  editValuesFromUser,
  hasUserChanges,
  userUpdateInit,
  validateEditUser,
  type EditUserValues,
} from '../../userForm';
import { FormErrorAlert } from '../FormErrorAlert';

/** Select value for "browser default" (Radix Select items cannot use ""). */
const DEFAULT_LOCALE = '__default__';

export interface EditUserDialogProps {
  user: User | undefined;
  onClose: () => void;
}

/** Edits display name, email, locale and the disabled flag of a user. */
export function EditUserDialog({ user, onClose }: EditUserDialogProps) {
  const { t } = useTranslation('access');
  return (
    <Dialog open={user !== undefined} onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('users.edit.title', { username: user?.username ?? '' })}</DialogTitle>
          <DialogDescription>{t('users.edit.description')}</DialogDescription>
        </DialogHeader>
        {user && <EditUserForm key={user.id} user={user} onDone={onClose} />}
      </DialogContent>
    </Dialog>
  );
}

function EditUserForm({ user, onDone }: { user: User; onDone: () => void }) {
  const { t } = useTranslation('access');
  const queryClient = useQueryClient();
  const { user: me, refresh } = useAuth();
  const disabledId = useId();
  const [values, setValues] = useState<EditUserValues>(() => editValuesFromUser(user));
  const [confirmDisable, setConfirmDisable] = useState(false);

  const mutation = useMutation({
    mutationFn: (form: EditUserValues) => accessClient.updateUser(userUpdateInit(user, form)),
    onSuccess: () => {
      toast.success(t('users.edit.success', { username: user.username }));
      void queryClient.invalidateQueries({ queryKey: ['access'] });
      // Editing one's own profile changes the signed-in user shown by the shell.
      if (user.id === me?.id) void refresh();
      onDone();
    },
  });

  const errors = validateEditUser(values);
  const changes = userUpdateInit(user, values);
  const set = <K extends keyof EditUserValues>(field: K, value: EditUserValues[K]) =>
    setValues((prev) => ({ ...prev, [field]: value }));

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (hasErrors(errors) || !hasUserChanges(changes)) return;
    // Disabling ends the user's sessions: ask first.
    if (changes.disabled === true) {
      setConfirmDisable(true);
      return;
    }
    mutation.mutate(values, { onError: (err) => toast.error(errorMessage(err, t)) });
  };

  return (
    <>
      <form className="grid gap-4" onSubmit={submit} noValidate>
        <FormField
          label={t('users.fields.displayName')}
          error={errors.displayName && t('users.errors.displayName')}
        >
          <Input value={values.displayName} onChange={(e) => set('displayName', e.target.value)} autoFocus />
        </FormField>
        <FormField
          label={t('users.fields.email')}
          description={t('users.edit.emailHint')}
          error={errors.email && t(`users.errors.email.${errors.email}`)}
        >
          <Input type="email" value={values.email} onChange={(e) => set('email', e.target.value)} />
        </FormField>
        <FormField label={t('users.fields.locale')} error={errors.locale && t('users.errors.locale')}>
          <Select
            value={values.locale === '' ? DEFAULT_LOCALE : values.locale}
            onValueChange={(locale) => set('locale', locale === DEFAULT_LOCALE ? '' : locale)}
          >
            <SelectTrigger className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={DEFAULT_LOCALE}>{t('users.edit.defaultLocale')}</SelectItem>
              {SUPPORTED_LANGUAGES.map((lng) => (
                <SelectItem key={lng} value={lng}>
                  {LANGUAGE_NAMES[lng]}
                </SelectItem>
              ))}
              {values.locale !== '' &&
                !(SUPPORTED_LANGUAGES as readonly string[]).includes(values.locale) && (
                  <SelectItem value={values.locale}>{values.locale}</SelectItem>
                )}
            </SelectContent>
          </Select>
        </FormField>
        <div className="flex items-start justify-between gap-4 rounded-md border px-3 py-2">
          <div className="grid gap-0.5">
            <Label htmlFor={disabledId}>{t('users.fields.disabled')}</Label>
            <p className="text-xs text-muted-foreground">{t('users.edit.disabledHint')}</p>
          </div>
          <Switch
            id={disabledId}
            checked={values.disabled}
            onCheckedChange={(checked) => set('disabled', checked)}
          />
        </div>
        <FormErrorAlert error={mutation.error} />
        <DialogFooter>
          <Button type="button" variant="outline" onClick={onDone} disabled={mutation.isPending}>
            {t('common:actions.cancel')}
          </Button>
          <Button
            type="submit"
            disabled={mutation.isPending || hasErrors(errors) || !hasUserChanges(changes)}
          >
            {mutation.isPending && <LoaderCircleIcon className="animate-spin" />}
            {t('common:actions.save')}
          </Button>
        </DialogFooter>
      </form>
      <ConfirmDialog
        open={confirmDisable}
        onOpenChange={setConfirmDisable}
        destructive
        title={t('users.disable.title', { username: user.username })}
        description={t('users.disable.description')}
        confirmLabel={t('users.disable.confirm')}
        onConfirm={() => mutation.mutateAsync(values)}
      />
    </>
  );
}
