import { BookOpenIcon, ExternalLinkIcon, MessageSquareWarningIcon, ShieldIcon } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { useAuth } from '@/app/auth/AuthContext';
import { PageHeader } from '@/components/PageHeader';
import { PageIntro } from '@/components/PageIntro';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { docsUrl, ISSUES_URL, REPO_URL, SECURITY_URL } from '@/lib/project';

import { RetentionCard } from '../components/RetentionCard';
import { UpdateCard } from '../components/UpdateCard';

function LinkRow({
  href,
  icon: Icon,
  label,
  hint,
}: {
  href: string;
  icon: typeof BookOpenIcon;
  label: string;
  hint: string;
}) {
  return (
    <a
      href={href}
      target="_blank"
      rel="noreferrer noopener"
      className="flex items-start gap-3 rounded-md px-2 py-2 outline-none transition-colors hover:bg-accent hover:text-accent-foreground focus-visible:ring-2 focus-visible:ring-ring"
    >
      <Icon className="mt-0.5 size-4 shrink-0 text-muted-foreground" aria-hidden />
      <span className="min-w-0 flex-1">
        <span className="block text-sm">{label}</span>
        <span className="block text-xs text-muted-foreground">{hint}</span>
      </span>
      <ExternalLinkIcon className="mt-0.5 size-3.5 shrink-0 text-muted-foreground" aria-hidden />
    </a>
  );
}

/** Deployment-level settings: which build is running, and where to get help. */
export default function SystemPage() {
  const { t, i18n } = useTranslation();
  const { isPlatformAdmin } = useAuth();

  return (
    <>
      <PageHeader title={t('system.title')} description={t('system.description')} />
      <PageIntro page="system" />
      <div className="grid gap-4 xl:grid-cols-2">
        <div className="grid content-start gap-4">
          <UpdateCard />
          <RetentionCard canEdit={isPlatformAdmin} />
        </div>
        <div className="grid content-start gap-4">
          <Card>
            <CardHeader>
              <CardTitle>{t('system.help.title')}</CardTitle>
            </CardHeader>
            <CardContent className="grid gap-0.5">
              <LinkRow
                href={docsUrl(i18n.language)}
                icon={BookOpenIcon}
                label={t('system.help.documentation')}
                hint={t('system.help.documentationHint')}
              />
              <LinkRow
                href={ISSUES_URL}
                icon={MessageSquareWarningIcon}
                label={t('system.help.issues')}
                hint={t('system.help.issuesHint')}
              />
              <LinkRow
                href={SECURITY_URL}
                icon={ShieldIcon}
                label={t('system.help.security')}
                hint={t('system.help.securityHint')}
              />
              <LinkRow
                href={REPO_URL}
                icon={BookOpenIcon}
                label={t('system.help.source')}
                hint={t('system.help.sourceHint')}
              />
            </CardContent>
          </Card>
        </div>
      </div>
    </>
  );
}
