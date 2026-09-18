import { Link } from '@tanstack/react-router';
import { FileQuestionIcon } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { usePageTitle } from '@/app/pageTitle';
import { Button } from '@/components/ui/button';

/** 404 page for unknown URLs. */
export function NotFoundPage() {
  const { t } = useTranslation();
  usePageTitle(t('notFound.title'));
  return (
    <div className="flex min-h-[60vh] flex-col items-center justify-center gap-3 px-6 text-center">
      <div className="flex size-12 items-center justify-center rounded-full bg-muted text-muted-foreground">
        <FileQuestionIcon className="size-6" aria-hidden />
      </div>
      <p className="font-mono text-sm text-muted-foreground">404</p>
      <h1 className="text-xl font-semibold">{t('notFound.title')}</h1>
      <p className="max-w-md text-sm text-muted-foreground">{t('notFound.description')}</p>
      <Button asChild className="mt-2">
        <Link to="/">{t('actions.goHome')}</Link>
      </Button>
    </div>
  );
}
