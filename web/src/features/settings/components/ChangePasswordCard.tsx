import { useMutation } from '@tanstack/react-query';
import { KeyRoundIcon, LoaderCircleIcon } from 'lucide-react';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { FormField } from '@/components/ui/form';
import { Input } from '@/components/ui/input';
import { authClient } from '@/lib/clients';
import { describeError } from '@/lib/errors';
import { sensitiveMutation } from '@/lib/sensitiveMutation';

/** Minimum new password length (ChangePasswordRequest.new_password min_len). */
export const MIN_PASSWORD_LENGTH = 10;

interface PasswordForm {
  current: string;
  next: string;
  confirm: string;
}

const EMPTY_FORM: PasswordForm = { current: '', next: '', confirm: '' };

type FieldErrors = Partial<Record<keyof PasswordForm, string>>;

/** Change password form (AuthService.ChangePassword). */
export function ChangePasswordCard() {
  const { t } = useTranslation();
  const [form, setForm] = useState<PasswordForm>(EMPTY_FORM);
  const [errors, setErrors] = useState<FieldErrors>({});

  // The variables hold both passwords.
  const mutation = useMutation(
    sensitiveMutation({
      mutationFn: (values: PasswordForm) =>
        authClient.changePassword({ currentPassword: values.current, newPassword: values.next }),
      onSuccess: () => {
        toast.success(t('profile.passwordChanged'));
        setForm(EMPTY_FORM);
        setErrors({});
      },
    }),
  );

  const update = (field: keyof PasswordForm, value: string) => {
    setForm((prev) => ({ ...prev, [field]: value }));
    setErrors((prev) => ({ ...prev, [field]: undefined }));
  };

  const validate = (values: PasswordForm): FieldErrors => {
    const next: FieldErrors = {};
    if (!values.current) next.current = t('validation.required');
    if (values.next.length < MIN_PASSWORD_LENGTH) {
      next.next = t('profile.passwordTooShort', { count: MIN_PASSWORD_LENGTH });
    } else if (values.next === values.current) {
      next.next = t('profile.passwordSame');
    }
    if (values.confirm !== values.next) next.confirm = t('profile.passwordMismatch');
    return next;
  };

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const found = validate(form);
    setErrors(found);
    if (Object.values(found).some(Boolean)) return;
    // The card stays mounted after a change: reset the observer so the finished
    // mutation (and the passwords in its variables) leaves the cache right away.
    mutation.mutate(form, { onSuccess: () => mutation.reset() });
  };

  const apiError = mutation.error ? describeError(mutation.error, t) : undefined;

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <KeyRoundIcon className="size-4 text-muted-foreground" aria-hidden />
          {t('profile.changePassword')}
        </CardTitle>
        <CardDescription>{t('profile.changePasswordDescription')}</CardDescription>
      </CardHeader>
      <CardContent>
        <form className="grid gap-4" onSubmit={submit} noValidate>
          <FormField label={t('profile.currentPassword')} error={errors.current} required>
            <Input
              type="password"
              autoComplete="current-password"
              value={form.current}
              onChange={(e) => update('current', e.target.value)}
            />
          </FormField>
          <FormField label={t('profile.newPassword')} error={errors.next} required>
            <Input
              type="password"
              autoComplete="new-password"
              value={form.next}
              onChange={(e) => update('next', e.target.value)}
            />
          </FormField>
          <FormField label={t('profile.confirmPassword')} error={errors.confirm} required>
            <Input
              type="password"
              autoComplete="new-password"
              value={form.confirm}
              onChange={(e) => update('confirm', e.target.value)}
            />
          </FormField>
          {apiError && (
            <p role="alert" className="text-sm text-destructive">
              {apiError.title}
              {apiError.detail && apiError.detail !== apiError.title && `: ${apiError.detail}`}
            </p>
          )}
          <div>
            <Button type="submit" disabled={mutation.isPending}>
              {mutation.isPending && <LoaderCircleIcon className="animate-spin" />}
              {t('profile.submit')}
            </Button>
          </div>
        </form>
      </CardContent>
    </Card>
  );
}
