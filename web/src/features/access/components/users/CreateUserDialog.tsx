import { useMutation, useQueryClient } from '@tanstack/react-query';
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
import { Separator } from '@/components/ui/separator';
import { accessClient } from '@/lib/clients';
import { errorMessage } from '@/lib/errors';
import { sensitiveMutation } from '@/lib/sensitiveMutation';

import { hasErrors } from '../../forms';
import {
  EMPTY_CREATE_USER,
  MIN_PASSWORD_LENGTH,
  toBindingInit,
  validateCreateUser,
  type CreateUserValues,
} from '../../userForm';
import { FormErrorAlert } from '../FormErrorAlert';

import { BindingFields } from './BindingFields';
import { PasswordInput } from './PasswordInput';

export interface CreateUserDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Display name of the active tenant (the initial binding is created there). */
  tenantName: string;
}

/** Creates a user with an initial role binding in the active tenant. */
export function CreateUserDialog({ open, onOpenChange, tenantName }: CreateUserDialogProps) {
  const { t } = useTranslation('access');
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{t('users.create.title')}</DialogTitle>
          <DialogDescription>{t('users.create.description', { tenant: tenantName })}</DialogDescription>
        </DialogHeader>
        <CreateUserForm onDone={() => onOpenChange(false)} />
      </DialogContent>
    </Dialog>
  );
}

function CreateUserForm({ onDone }: { onDone: () => void }) {
  const { t } = useTranslation('access');
  const queryClient = useQueryClient();
  const [values, setValues] = useState<CreateUserValues>(EMPTY_CREATE_USER);
  const [submitted, setSubmitted] = useState(false);

  // The variables hold the initial password.
  const mutation = useMutation(
    sensitiveMutation({
      mutationFn: (form: CreateUserValues) =>
        accessClient.createUser({
          username: form.username,
          displayName: form.displayName.trim(),
          email: form.email.trim(),
          password: form.password,
          ...toBindingInit(form.binding),
        }),
      onSuccess: (response) => {
        toast.success(t('users.create.success', { username: response.user?.username ?? '' }));
        void queryClient.invalidateQueries({ queryKey: ['access'] });
        onDone();
      },
      onError: (err) => toast.error(errorMessage(err, t)),
    }),
  );

  const errors = validateCreateUser(values);
  const shown = submitted ? errors : {};
  const set = <K extends keyof CreateUserValues>(field: K, value: CreateUserValues[K]) =>
    setValues((prev) => ({ ...prev, [field]: value }));

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setSubmitted(true);
    if (hasErrors(errors)) return;
    mutation.mutate(values);
  };

  return (
    <form className="grid gap-4" onSubmit={submit} noValidate>
      <div className="grid gap-4 sm:grid-cols-2">
        <FormField
          label={t('users.fields.username')}
          required
          description={t('users.fields.usernameHint')}
          error={shown.username && t(`users.errors.username.${shown.username}`)}
        >
          <Input
            value={values.username}
            onChange={(e) => set('username', e.target.value)}
            autoComplete="off"
            spellCheck={false}
            className="font-mono"
            autoFocus
          />
        </FormField>
        <FormField
          label={t('users.fields.displayName')}
          error={shown.displayName && t('users.errors.displayName')}
        >
          <Input value={values.displayName} onChange={(e) => set('displayName', e.target.value)} />
        </FormField>
        <FormField
          label={t('users.fields.email')}
          error={shown.email && t(`users.errors.email.${shown.email}`)}
        >
          <Input
            type="email"
            value={values.email}
            onChange={(e) => set('email', e.target.value)}
            autoComplete="off"
          />
        </FormField>
        <FormField
          label={t('users.fields.password')}
          required
          description={t('password.hint', { count: MIN_PASSWORD_LENGTH })}
          error={shown.password && t(`password.errors.${shown.password}`, { count: MIN_PASSWORD_LENGTH })}
        >
          <PasswordInput value={values.password} onChange={(v) => set('password', v)} showStrength />
        </FormField>
      </div>
      <Separator />
      <div className="grid gap-1">
        <p className="text-sm font-medium">{t('users.create.bindingTitle')}</p>
        <p className="text-xs text-muted-foreground">{t('users.create.bindingDescription')}</p>
      </div>
      <BindingFields
        value={values.binding}
        onChange={(binding) => set('binding', binding)}
        errors={shown}
        disabled={mutation.isPending}
      />
      <FormErrorAlert error={mutation.error} />
      <DialogFooter>
        <Button type="button" variant="outline" onClick={onDone} disabled={mutation.isPending}>
          {t('common:actions.cancel')}
        </Button>
        <Button type="submit" disabled={mutation.isPending}>
          {mutation.isPending && <LoaderCircleIcon className="animate-spin" />}
          {t('users.create.submit')}
        </Button>
      </DialogFooter>
    </form>
  );
}
