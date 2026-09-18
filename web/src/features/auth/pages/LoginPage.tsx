import { Code } from '@connectrpc/connect';
import { useRouter, useSearch } from '@tanstack/react-router';
import { LoaderCircleIcon, LogInIcon, TriangleAlertIcon } from 'lucide-react';
import { useEffect, useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';

import { useAuth } from '@/app/auth/AuthContext';
import { LanguageSwitcher } from '@/components/LanguageSwitcher';
import { ThemeToggle } from '@/components/ThemeToggle';
import { Button } from '@/components/ui/button';
import { Card } from '@/components/ui/card';
import { FormField } from '@/components/ui/form';
import { Input } from '@/components/ui/input';
import { humanizeDuration } from '@/lib/duration';
import { describeError, toApiError } from '@/lib/errors';

import { safeRedirect } from '../redirect';

type LoginError = { kind: 'required' } | { kind: 'api'; error: unknown };

function useLoginErrorMessage(error: LoginError | undefined): { title: string; detail?: string } | undefined {
  const { t } = useTranslation();
  if (!error) return undefined;
  if (error.kind === 'required') return { title: t('login.required') };
  const api = toApiError(error.error);
  if (api.reason === 'login_throttled') {
    return {
      title:
        api.retryAfterMs !== undefined
          ? t('login.throttled', { time: humanizeDuration(api.retryAfterMs, { t }) })
          : t('login.throttledNoTime'),
    };
  }
  if (api.code === Code.Unauthenticated) return { title: t('login.invalidCredentials') };
  const described = describeError(error.error, t);
  return { title: described.title, detail: described.detail };
}

/** Console sign-in page (AuthService.Login). */
export default function LoginPage() {
  const { t } = useTranslation();
  const router = useRouter();
  const { status, login } = useAuth();
  const search = useSearch({ from: '/login' });
  const redirectTo = safeRedirect(search.redirect);

  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState<LoginError>();
  const [pending, setPending] = useState(false);
  const message = useLoginErrorMessage(error);

  // Already signed in (or just signed in): leave the login page.
  useEffect(() => {
    if (status === 'authenticated') {
      router.history.replace(redirectTo);
    }
  }, [status, redirectTo, router]);

  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!username.trim() || !password) {
      setError({ kind: 'required' });
      return;
    }
    setPending(true);
    setError(undefined);
    try {
      await login(username.trim(), password);
    } catch (err) {
      setError({ kind: 'api', error: err });
      setPassword('');
    } finally {
      setPending(false);
    }
  };

  return (
    <div className="relative flex min-h-full flex-col items-center justify-center bg-muted/40 px-4 py-10">
      <div className="absolute top-3 right-3 flex items-center gap-1">
        <LanguageSwitcher />
        <ThemeToggle />
      </div>
      <div className="mb-6 flex flex-col items-center gap-2 text-center">
        <img src="/favicon.svg" alt="" className="size-11" />
        <div className="text-xl font-semibold tracking-tight">{t('app.name')}</div>
        <div className="text-sm text-muted-foreground">{t('app.tagline')}</div>
      </div>
      <Card className="w-full max-w-sm p-6">
        <div className="mb-5 space-y-1">
          <h1 className="text-lg font-semibold">{t('login.title')}</h1>
          <p className="text-sm text-muted-foreground">{t('login.subtitle')}</p>
        </div>
        <form className="grid gap-4" onSubmit={(e) => void submit(e)} noValidate>
          <FormField label={t('login.username')}>
            <Input
              name="username"
              autoComplete="username"
              autoFocus
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              disabled={pending}
            />
          </FormField>
          <FormField label={t('login.password')}>
            <Input
              name="password"
              type="password"
              autoComplete="current-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              disabled={pending}
            />
          </FormField>
          {message && (
            <div
              role="alert"
              className="flex gap-2 rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm text-destructive"
            >
              <TriangleAlertIcon className="mt-0.5 size-4 shrink-0" aria-hidden />
              <div className="min-w-0">
                <p>{message.title}</p>
                {message.detail && message.detail !== message.title && (
                  <p className="mt-0.5 text-xs break-words opacity-80">{message.detail}</p>
                )}
              </div>
            </div>
          )}
          <Button type="submit" disabled={pending} className="w-full">
            {pending ? <LoaderCircleIcon className="animate-spin" /> : <LogInIcon />}
            {pending ? t('login.submitting') : t('login.submit')}
          </Button>
        </form>
      </Card>
      <p className="mt-6 max-w-sm text-center text-xs text-muted-foreground">{t('login.footer')}</p>
    </div>
  );
}
