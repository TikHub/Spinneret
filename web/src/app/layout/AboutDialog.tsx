import { useTranslation } from 'react-i18next';

import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog';
import { COPYRIGHT_YEAR, LICENSE, LICENSE_URL, MAINTAINER, MAINTAINER_URL, REPO_URL } from '@/lib/project';

export interface AboutDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

/**
 * Who made this and under what terms — copyright, licence, maintainer.
 *
 * Deliberately nothing else: the build number, the update check and the links an
 * operator needs while running the thing live on Settings → System, which is a
 * page they can link to and come back to.
 */
export function AboutDialog({ open, onOpenChange }: AboutDialogProps) {
  const { t } = useTranslation();

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <img src="/favicon.svg" alt="" className="size-6 shrink-0" />
            {t('app.name')}
          </DialogTitle>
          <DialogDescription>{t('app.tagline')}</DialogDescription>
        </DialogHeader>

        <dl className="grid grid-cols-[auto_1fr] items-baseline gap-x-4 gap-y-1.5 text-sm">
          <dt className="text-muted-foreground">{t('about.copyright')}</dt>
          <dd>{t('about.copyrightValue', { year: COPYRIGHT_YEAR, maintainer: MAINTAINER })}</dd>
          <dt className="text-muted-foreground">{t('about.license')}</dt>
          <dd>
            <a
              href={LICENSE_URL}
              target="_blank"
              rel="noreferrer noopener"
              className="underline underline-offset-4 hover:text-primary"
            >
              {LICENSE}
            </a>
          </dd>
          <dt className="text-muted-foreground">{t('about.maintainer')}</dt>
          <dd>
            <a
              href={MAINTAINER_URL}
              target="_blank"
              rel="noreferrer noopener"
              className="underline underline-offset-4 hover:text-primary"
            >
              {MAINTAINER}
            </a>
          </dd>
          <dt className="text-muted-foreground">{t('about.source')}</dt>
          <dd>
            <a
              href={REPO_URL}
              target="_blank"
              rel="noreferrer noopener"
              className="underline underline-offset-4 hover:text-primary"
            >
              {t('about.sourceValue')}
            </a>
          </dd>
        </dl>

        <p className="text-xs text-muted-foreground">{t('about.openSource', { maintainer: MAINTAINER })}</p>
      </DialogContent>
    </Dialog>
  );
}
