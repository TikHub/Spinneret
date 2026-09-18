import { useMutation, useQueryClient } from '@tanstack/react-query';
import { LoaderCircleIcon, PlusIcon } from 'lucide-react';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import { useAuth } from '@/app/auth/AuthContext';
import { Button } from '@/components/ui/button';
import { type User } from '@/gen/spinneret/v1/auth_pb';
import { accessClient } from '@/lib/clients';
import { errorMessage } from '@/lib/errors';

import { hasErrors } from '../../forms';
import { EMPTY_BINDING, toBindingInit, validateBinding, type BindingFormValues } from '../../userForm';
import { FormErrorAlert } from '../FormErrorAlert';

import { BindingFields } from './BindingFields';

export interface AddBindingFormProps {
  user: User;
  onCancel: () => void;
  onAdded: () => void;
}

/** Grants a role in the active tenant to an existing user (CreateRoleBinding). */
export function AddBindingForm({ user, onCancel, onAdded }: AddBindingFormProps) {
  const { t } = useTranslation('access');
  const queryClient = useQueryClient();
  const { user: me, refresh } = useAuth();
  const [values, setValues] = useState<BindingFormValues>(EMPTY_BINDING);
  const [submitted, setSubmitted] = useState(false);

  const mutation = useMutation({
    mutationFn: (form: BindingFormValues) =>
      accessClient.createRoleBinding({ userId: user.id, ...toBindingInit(form) }),
    onSuccess: () => {
      toast.success(t('bindings.added', { username: user.username }));
      void queryClient.invalidateQueries({ queryKey: ['access'] });
      // The caller's own permissions (and switchers) change with their bindings.
      if (user.id === me?.id) void refresh();
      onAdded();
    },
    onError: (err) => toast.error(errorMessage(err, t)),
  });

  const errors = validateBinding(values);

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setSubmitted(true);
    if (hasErrors(errors)) return;
    mutation.mutate(values);
  };

  return (
    <form
      className="grid gap-4 rounded-md border bg-muted/20 p-3"
      onSubmit={submit}
      noValidate
      aria-label={t('bindings.addTitle')}
    >
      <p className="text-sm font-medium">{t('bindings.addTitle')}</p>
      <BindingFields
        value={values}
        onChange={setValues}
        errors={submitted ? errors : {}}
        disabled={mutation.isPending}
      />
      <FormErrorAlert error={mutation.error} />
      <div className="flex justify-end gap-2">
        <Button type="button" variant="outline" size="sm" onClick={onCancel} disabled={mutation.isPending}>
          {t('common:actions.cancel')}
        </Button>
        <Button type="submit" size="sm" disabled={mutation.isPending}>
          {mutation.isPending ? <LoaderCircleIcon className="animate-spin" /> : <PlusIcon />}
          {t('bindings.add')}
        </Button>
      </div>
    </form>
  );
}
