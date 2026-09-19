import { render, screen, type RenderResult } from '@testing-library/react';
import i18next from 'i18next';
import { I18nextProvider, initReactI18next } from 'react-i18next';
import { describe, expect, it, vi } from 'vitest';

import common from '@/i18n/locales/en/common.json';
import { docsUrl, MAINTAINER_URL, REPO_URL, SECURITY_URL } from '@/lib/project';

import { AboutDialog } from './AboutDialog';

const serverVersion = vi.hoisted(() => ({ value: 'v1.2.3' }));

vi.mock('@/app/auth/AuthContext', () => ({
  useAuth: () => ({ serverVersion: serverVersion.value }),
}));

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
  it('names the maintainer, the licence and the server build', async () => {
    serverVersion.value = 'v1.2.3';
    await open();
    expect(screen.getByText('TikHub')).toBeInTheDocument();
    expect(screen.getByText('Apache-2.0')).toBeInTheDocument();
    expect(screen.getByText('v1.2.3')).toBeInTheDocument();
  });

  it('says the version is unknown rather than showing an empty row', async () => {
    // GetMe has not answered yet, or an older server did not send the field.
    serverVersion.value = '';
    await open();
    expect(screen.getByText('unknown')).toBeInTheDocument();
  });

  it('opens every external link in a new tab without leaking a referrer', async () => {
    serverVersion.value = 'v1.2.3';
    await open();
    const links = screen.getAllByRole('link');
    expect(links.length).toBeGreaterThan(0);
    for (const link of links) {
      expect(link).toHaveAttribute('target', '_blank');
      // Both tokens matter: noopener for the opener reference, noreferrer for the header.
      expect(link.getAttribute('rel')).toContain('noopener');
      expect(link.getAttribute('rel')).toContain('noreferrer');
      // Every link is either the organisation page or something under the repository.
      const href = link.getAttribute('href') ?? '';
      expect(href === MAINTAINER_URL || href.startsWith(`${REPO_URL}/`) || href === REPO_URL).toBe(true);
    }
  });

  it('links the repository, the manual and the security policy', async () => {
    serverVersion.value = 'v1.2.3';
    await open();
    const hrefs = screen.getAllByRole('link').map((l) => l.getAttribute('href'));
    expect(hrefs).toContain(REPO_URL);
    expect(hrefs).toContain(SECURITY_URL);
    // The test environment runs in English, so the manual resolves to documents/en.
    expect(hrefs).toContain(docsUrl('en'));
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
