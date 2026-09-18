import { Link, useMatches } from '@tanstack/react-router';
import { ChevronRightIcon } from 'lucide-react';
import { Fragment } from 'react';
import { useTranslation } from 'react-i18next';

interface Crumb {
  label: string;
  to?: string;
}

/** Breadcrumbs derived from the deepest route with staticData.titleKey. */
export function Breadcrumbs() {
  const { t } = useTranslation();
  const matches = useMatches();
  const match = [...matches].reverse().find((m) => m.staticData?.titleKey);
  if (!match?.staticData.titleKey) return null;

  const meta = match.staticData;
  const crumbs: Crumb[] = [];
  if (meta.groupKey) crumbs.push({ label: t(meta.groupKey) });
  if (meta.parent) crumbs.push({ label: t(meta.parent.titleKey), to: meta.parent.to });
  crumbs.push({ label: t(meta.titleKey ?? '', match.params as Record<string, string>) });

  return (
    <nav aria-label={t('shell.breadcrumbs')} className="min-w-0">
      <ol className="flex min-w-0 items-center gap-1.5 text-sm text-muted-foreground">
        {crumbs.map((crumb, index) => {
          const last = index === crumbs.length - 1;
          return (
            <Fragment key={`${crumb.label}-${index}`}>
              {index > 0 && <ChevronRightIcon className="size-3.5 shrink-0" aria-hidden />}
              <li
                className={last ? 'truncate font-medium text-foreground' : 'shrink-0'}
                aria-current={last ? 'page' : undefined}
              >
                {crumb.to && !last ? (
                  <Link to={crumb.to as never} className="hover:text-foreground">
                    {crumb.label}
                  </Link>
                ) : (
                  crumb.label
                )}
              </li>
            </Fragment>
          );
        })}
      </ol>
    </nav>
  );
}
