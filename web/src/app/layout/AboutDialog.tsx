import {
  BookOpenIcon,
  ExternalLinkIcon,
  FileTextIcon,
  MessageSquareWarningIcon,
  ScaleIcon,
  ShieldIcon,
  type LucideIcon,
} from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { useAuth } from '@/app/auth/AuthContext';
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog';
import {
  CHANGELOG_URL,
  ISSUES_URL,
  LICENSE,
  LICENSE_URL,
  MAINTAINER,
  MAINTAINER_URL,
  REPO_URL,
  SECURITY_URL,
  docsUrl,
} from '@/lib/project';

export interface AboutDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

/** One external link row. Opens in a new tab and leaks no referrer. */
function LinkRow({ href, icon: Icon, label }: { href: string; icon: LucideIcon; label: string }) {
  return (
    <a
      href={href}
      target="_blank"
      rel="noreferrer noopener"
      className="flex items-center gap-2.5 rounded-md px-2 py-1.5 text-sm outline-none transition-colors hover:bg-accent hover:text-accent-foreground focus-visible:ring-2 focus-visible:ring-ring"
    >
      <Icon className="size-4 shrink-0 text-muted-foreground" aria-hidden />
      <span className="min-w-0 flex-1 truncate">{label}</span>
      <ExternalLinkIcon className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
    </a>
  );
}

/**
 * What this deployment is, which build it runs, and where the source, manual and
 * reporting channels are. Reached from the button at the foot of the sidebar.
 */
export function AboutDialog({ open, onOpenChange }: AboutDialogProps) {
  const { t, i18n } = useTranslation();
  const { serverVersion } = useAuth();

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <img src="/favicon.svg" alt="" className="size-6 shrink-0" />
            {t('app.name')}
          </DialogTitle>
          <DialogDescription>{t('app.tagline')}</DialogDescription>
        </DialogHeader>

        <p className="text-sm text-muted-foreground">{t('about.summary')}</p>

        <dl className="grid grid-cols-[auto_1fr] items-baseline gap-x-4 gap-y-1.5 rounded-md border bg-muted/40 px-3 py-2 text-sm">
          <dt className="text-muted-foreground">{t('about.serverVersion')}</dt>
          <dd className="truncate font-mono text-xs">{serverVersion || t('about.versionUnknown')}</dd>
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
        </dl>

        <nav className="grid gap-0.5" aria-label={t('about.links')}>
          <LinkRow href={REPO_URL} icon={BookOpenIcon} label={t('about.repository')} />
          <LinkRow href={docsUrl(i18n.language)} icon={FileTextIcon} label={t('about.documentation')} />
          <LinkRow href={CHANGELOG_URL} icon={ScaleIcon} label={t('about.changelog')} />
          <LinkRow href={ISSUES_URL} icon={MessageSquareWarningIcon} label={t('about.issues')} />
          <LinkRow href={SECURITY_URL} icon={ShieldIcon} label={t('about.security')} />
        </nav>

        <p className="text-xs text-muted-foreground">{t('about.openSource', { maintainer: MAINTAINER })}</p>
      </DialogContent>
    </Dialog>
  );
}
