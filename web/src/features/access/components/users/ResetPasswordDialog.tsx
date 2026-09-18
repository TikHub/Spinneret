import { useMutation } from '@tanstack/react-query';
import { LoaderCircleIcon, TriangleAlertIcon } from 'lucide-react';
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
import { type User } from '@/gen/spinneret/v1/auth_pb';
import { accessClient } from '@/lib/clients';
import { errorMessage } from '@/lib/errors';
import { sensitiveMutation } from '@/lib/sensitiveMutation';

import { MIN_PASSWORD_LENGTH, validatePassword } from '../../userForm';
import { FormErrorAlert } from '../FormErrorAlert';

import { PasswordInput } from './PasswordInput';

export interface ResetPasswordDialogProps {
  user: User | undefined;
  onClose: () => void;
}

/** Sets a new password for a user; the server ends the user's sessions. */
export function ResetPasswordDialog({ user, onClose }: ResetPasswordDialogProps) {
  const { t } = useTranslation('access');
  return (
    <Dialog open={user !== undefined} onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('users.resetPassword.title', { username: user?.username ?? '' })}</DialogTitle>
          <DialogDescription>{t('users.resetPassword.description')}</DialogDescription>
        </DialogHeader>
        {user && <ResetPasswordForm key={user.id} user={user} onDone={onClose} />}
      </DialogContent>
    </Dialog>
  );
}

function ResetPasswordForm({ user, onDone }: { user: User; onDone: () => void }) {
  const { t } = useTranslation('access');
  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const [submitted, setSubmitted] = useState(false);

  // The mutation function closes over the new password.
  const mutation = useMutation(
    sensitiveMutation({
      mutationFn: () => accessClient.resetPassword({ userId: user.id, newPassword: password }),
      onSuccess: () => {
        toast.success(t('users.resetPassword.success', { username: user.username }));
        onDone();
      },
      onError: (err) => toast.error(errorMessage(err, t)),
    }),
  );

  const passwordError = validatePassword(password);
  const mismatch = confirm !== password;

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setSubmitted(true);
    if (passwordError || mismatch) return;
    mutation.mutate();
  };

  return (
    <form className="grid gap-4" onSubmit={submit} noValidate>
      <div
        role="note"
        className="flex gap-2 rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-sm text-amber-800 dark:text-amber-300"
      >
        <TriangleAlertIcon className="mt-0.5 size-4 shrink-0" aria-hidden />
        <p>{t('users.resetPassword.warning')}</p>
      </div>
      <FormField
        label={t('users.resetPassword.newPassword')}
        required
        description={t('password.hint', { count: MIN_PASSWORD_LENGTH })}
        error={
          submitted && passwordError
            ? t(`password.errors.${passwordError}`, { count: MIN_PASSWORD_LENGTH })
            : undefined
        }
      >
        <PasswordInput value={password} onChange={setPassword} showStrength />
      </FormField>
      <FormField
        label={t('users.resetPassword.confirmPassword')}
        required
        error={submitted && mismatch ? t('common:validation.mismatch') : undefined}
      >
        <PasswordInput value={confirm} onChange={setConfirm} />
      </FormField>
      <FormErrorAlert error={mutation.error} />
      <DialogFooter>
        <Button type="button" variant="outline" onClick={onDone} disabled={mutation.isPending}>
          {t('common:actions.cancel')}
        </Button>
        <Button type="submit" variant="destructive" disabled={mutation.isPending}>
          {mutation.isPending && <LoaderCircleIcon className="animate-spin" />}
          {t('users.resetPassword.submit')}
        </Button>
      </DialogFooter>
    </form>
  );
}
