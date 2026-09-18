import { Link, useRouter, type LinkProps } from '@tanstack/react-router';
import { ChevronDownIcon, InfoIcon } from 'lucide-react';
import { useId, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';

import { readStorage, writeStorage } from '@/lib/storage';
import { cn } from '@/lib/utils';

/** Link to a related console page shown at the bottom of the card. */
export interface PageIntroLink {
  /** Router path, e.g. '/sites'. */
  to: LinkProps['to'];
  /** Label key resolved in the `common` namespace, e.g. 'nav.sites'. */
  labelKey: string;
}

export interface PageIntroProps {
  /**
   * Page identifier. Copy is read from the `intro` namespace under
   * `pages.<page>` and the collapsed state is stored per page.
   */
  page: string;
  /** Related pages linked under the body text. */
  links?: readonly PageIntroLink[];
  /** Extra content appended to the body (e.g. the tenancy diagram). */
  children?: ReactNode;
  className?: string;
}

/** localStorage key holding the collapsed state of one page's intro card. */
function storageKey(page: string): string {
  return `spinneret.intro.${page}`;
}

/** Reads the persisted state; the card is expanded unless it was collapsed. */
function readExpanded(page: string): boolean {
  return readStorage(storageKey(page)) !== '0';
}

/** Reads a translated string array, tolerating a missing or malformed value. */
function useBullets(page: string): string[] {
  const { t } = useTranslation('intro');
  const raw: unknown = t(`pages.${page}.steps`, { returnObjects: true });
  if (!Array.isArray(raw)) return [];
  return raw.filter((item): item is string => typeof item === 'string');
}

/**
 * Collapsible "what this page is for" card rendered directly under the page
 * header. It explains what the page controls, how it is normally used and a
 * concrete situation that calls for it. The collapsed state is remembered per
 * page in localStorage so the card can be dismissed once and stays that way.
 */
export function PageIntro({ page, links, children, className }: PageIntroProps) {
  const { t } = useTranslation('intro');
  const [expanded, setExpanded] = useState(() => readExpanded(page));
  const bodyId = useId();
  const headingId = useId();
  const bullets = useBullets(page);
  // Router links need a RouterProvider; unit tests render the card without one.
  const router: unknown = useRouter({ warn: false });
  const showLinks = Boolean(router) && links !== undefined && links.length > 0;

  const toggle = () => {
    setExpanded((previous) => {
      const next = !previous;
      writeStorage(storageKey(page), next ? '1' : '0');
      return next;
    });
  };

  return (
    <section
      data-page-intro={page}
      aria-labelledby={headingId}
      className={cn('mb-4 rounded-lg border bg-muted/40', className)}
    >
      <button
        type="button"
        onClick={toggle}
        aria-expanded={expanded}
        aria-controls={bodyId}
        aria-label={expanded ? t('collapseLabel') : t('expandLabel')}
        className="flex w-full items-center gap-2 rounded-lg px-3 py-2 text-left focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
      >
        <InfoIcon className="size-4 shrink-0 text-muted-foreground" aria-hidden />
        <span id={headingId} className="min-w-0 flex-1 truncate text-sm font-medium">
          {t('label')}
        </span>
        <span className="hidden shrink-0 text-xs text-muted-foreground sm:inline">
          {expanded ? t('collapse') : t('expand')}
        </span>
        <ChevronDownIcon
          className={cn(
            'size-4 shrink-0 text-muted-foreground transition-transform',
            expanded && 'rotate-180',
          )}
          aria-hidden
        />
      </button>
      <div id={bodyId} hidden={!expanded} className="space-y-2 px-3 pb-3 text-sm text-muted-foreground">
        <p className="max-w-4xl">{t(`pages.${page}.summary`)}</p>
        {bullets.length > 0 && (
          <ul className="ml-4 max-w-4xl list-disc space-y-1">
            {bullets.map((bullet) => (
              <li key={bullet}>{bullet}</li>
            ))}
          </ul>
        )}
        <p className="max-w-4xl">
          <span className="font-medium text-foreground">{t('whenLabel')}</span> {t(`pages.${page}.when`)}
        </p>
        {children}
        {showLinks && links && (
          <p className="flex flex-wrap items-center gap-x-3 gap-y-1">
            <span className="text-xs uppercase tracking-wide">{t('relatedLabel')}</span>
            {links.map((link) => (
              <Link
                key={String(link.to)}
                to={link.to}
                className="text-sm font-medium text-foreground underline underline-offset-4 hover:text-primary"
              >
                {t(link.labelKey)}
              </Link>
            ))}
          </p>
        )}
      </div>
    </section>
  );
}

export interface PageIntroSlotProps {
  /** Page content whose first element is the PageHeader. */
  children: ReactNode;
  /** The <PageIntro> to show between that header and the rest of the content. */
  intro: ReactNode;
}

/**
 * Places a PageIntro under a PageHeader that is rendered inside the page's own
 * content component (heatmap, request explorer) instead of the route file.
 * The wrapper is a flex column: the first child (the header) keeps order 1, the
 * intro is pulled to order 2 and every other child follows, so the DOM order
 * stays "content, intro" while the visual order is "header, intro, content".
 */
export function PageIntroSlot({ children, intro }: PageIntroSlotProps) {
  return (
    <div className="flex min-w-0 flex-col [&>*:first-child]:order-1 [&>[data-page-intro]]:order-2 [&>*]:order-3 [&>*]:min-w-0">
      {children}
      {intro}
    </div>
  );
}
