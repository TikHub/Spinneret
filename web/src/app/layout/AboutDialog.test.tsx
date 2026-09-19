import { render, screen, type RenderResult } from '@testing-library/react';
import i18next from 'i18next';
import { I18nextProvider, initReactI18next } from 'react-i18next';
import { describe, expect, it } from 'vitest';

import common from '@/i18n/locales/en/common.json';
import { docsUrl, MAINTAINER_URL, REPO_URL } from '@/lib/project';

import { AboutDialog } from './AboutDialog';

/** The dialog reads copy and the active language from i18n, so it needs a real instance. */
async function open(): Promise<RenderResult> {
  const i18n = i18next.createInstance();
  await i18n.use(initReactI18next).init({
    lng: 'en',
    resources: { en: { common } },
    ns: ['common'],
    defaultNS: 'common',
    interpolation: { escapeValue: false },
    react: { useSuspense: false },
  });
  return render(
    <I18nextProvider i18n={i18n}>
      <AboutDialog open onOpenChange={() => {}} />
    </I18nextProvider>,
  );
}

describe('AboutDialog', () => {
  it('states the copyright, the licence and the maintainer', async () => {
    await open();
    expect(screen.getByText('© 2026 TikHub')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Apache-2.0' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'TikHub' })).toBeInTheDocument();
  });

  it('carries no version and no update check — those belong on Settings → System', async () => {
    await open();
    expect(screen.queryByText(/Server build/i)).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /check for updates/i })).not.toBeInTheDocument();
  });

  it('opens every external link in a new tab without leaking a referrer', async () => {
    await open();
    const links = screen.getAllByRole('link');
    expect(links.length).toBeGreaterThan(0);
    for (const link of links) {
      expect(link).toHaveAttribute('target', '_blank');
      // Both tokens matter: noopener for the opener reference, noreferrer for the header.
      expect(link.getAttribute('rel')).toContain('noopener');
      expect(link.getAttribute('rel')).toContain('noreferrer');
      const href = link.getAttribute('href') ?? '';
      expect(href === MAINTAINER_URL || href === REPO_URL || href.startsWith(`${REPO_URL}/`)).toBe(true);
    }
  });
});

describe('docsUrl', () => {
  it('sends Chinese readers to the Chinese manual and everyone else to the English one', () => {
    expect(docsUrl('zh-CN')).toBe(`${REPO_URL}/tree/main/documents/zh`);
    expect(docsUrl('zh')).toBe(`${REPO_URL}/tree/main/documents/zh`);
    expect(docsUrl('en')).toBe(`${REPO_URL}/tree/main/documents/en`);
    expect(docsUrl('de')).toBe(`${REPO_URL}/tree/main/documents/en`);
    // An i18n instance that has not finished initialising reports no language.
    expect(docsUrl(undefined)).toBe(`${REPO_URL}/tree/main/documents/en`);
  });
});
