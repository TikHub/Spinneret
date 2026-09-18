import { useMatches } from '@tanstack/react-router';
import { useEffect } from 'react';
import { useTranslation } from 'react-i18next';

import { usePageTitleOverride } from '@/app/pageTitle';

/** Keeps document.title in sync with the active route ("<page> · Spinneret"). */
export function DocumentTitle() {
  const { t } = useTranslation();
  const matches = useMatches();
  const override = usePageTitleOverride();
  const match = [...matches].reverse().find((m) => m.staticData?.titleKey);
  const routeTitle = match?.staticData.titleKey
    ? t(match.staticData.titleKey, match.params as Record<string, string>)
    : undefined;
  const title = override ?? routeTitle;
  const appName = t('app.name');

  useEffect(() => {
    document.title = title ? `${title} · ${appName}` : appName;
  }, [title, appName]);

  return null;
}
