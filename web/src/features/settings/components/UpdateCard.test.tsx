import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen, type RenderResult } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import i18next from 'i18next';
import { I18nextProvider, initReactI18next } from 'react-i18next';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import common from '@/i18n/locales/en/common.json';

const checkForUpdate = vi.hoisted(() => vi.fn());

vi.mock('@/lib/clients', () => ({ systemClient: { checkForUpdate } }));
vi.mock('@/app/auth/AuthContext', () => ({ useAuth: () => ({ serverVersion: 'v0.1.0' }) }));

const { UpdateCard } = await import('./UpdateCard');

async function renderCard(): Promise<RenderResult> {
  const i18n = i18next.createInstance();
  await i18n.use(initReactI18next).init({
    lng: 'en',
    resources: { en: { common } },
    ns: ['common'],
    defaultNS: 'common',
    interpolation: { escapeValue: false },
    react: { useSuspense: false },
  });
  const client = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <I18nextProvider i18n={i18n}>
        <UpdateCard />
      </I18nextProvider>
    </QueryClientProvider>,
  );
}

beforeEach(() => checkForUpdate.mockReset());

describe('UpdateCard', () => {
  it('shows the running build before anything is checked, and checks nothing on its own', async () => {
    await renderCard();
    expect(screen.getByText('v0.1.0')).toBeInTheDocument();
    // The deployment must not reach out until an operator asks it to.
    expect(checkForUpdate).not.toHaveBeenCalled();
  });

  it('offers the upgrade and links its release notes', async () => {
    checkForUpdate.mockResolvedValue({
      currentVersion: 'v0.1.0',
      latestVersion: 'v0.2.0',
      releaseUrl: 'https://github.com/TikHub/Spinneret/releases/tag/v0.2.0',
      updateAvailable: true,
      currentIsRelease: true,
      disabled: false,
      error: '',
    });
    await renderCard();
    await userEvent.click(screen.getByRole('button', { name: /check for updates/i }));

    expect(await screen.findByText('v0.2.0 is available')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /release notes/i })).toHaveAttribute(
      'href',
      'https://github.com/TikHub/Spinneret/releases/tag/v0.2.0',
    );
  });

  it('says so when the build is current', async () => {
    checkForUpdate.mockResolvedValue({
      currentVersion: 'v0.2.0',
      latestVersion: 'v0.2.0',
      updateAvailable: false,
      currentIsRelease: true,
      disabled: false,
      error: '',
    });
    await renderCard();
    await userEvent.click(screen.getByRole('button', { name: /check for updates/i }));
    expect(await screen.findByText('This is the latest release.')).toBeInTheDocument();
  });

  it('distinguishes "nothing published yet" from "the build is current"', async () => {
    checkForUpdate.mockResolvedValue({
      currentVersion: 'v0.1.0',
      latestVersion: '',
      updateAvailable: false,
      currentIsRelease: true,
      disabled: false,
      error: '',
    });
    await renderCard();
    await userEvent.click(screen.getByRole('button', { name: /check for updates/i }));
    expect(await screen.findByText('No release has been published yet.')).toBeInTheDocument();
  });

  it('keeps the version visible when the feed could not be read', async () => {
    // The RPC succeeds and reports the reason, so the operator does not lose the
    // build number just because the network refused.
    checkForUpdate.mockResolvedValue({
      currentVersion: 'v0.1.0',
      latestVersion: '',
      updateAvailable: false,
      disabled: false,
      error: 'reach the release feed: dial tcp: i/o timeout',
    });
    await renderCard();
    await userEvent.click(screen.getByRole('button', { name: /check for updates/i }));

    expect(await screen.findByText(/could not be read/i)).toBeInTheDocument();
    expect(screen.getByText('v0.1.0')).toBeInTheDocument();
  });

  it('hides the button and explains itself when the operator disabled the check', async () => {
    checkForUpdate.mockResolvedValue({
      currentVersion: 'v0.1.0',
      latestVersion: '',
      updateAvailable: false,
      currentIsRelease: true,
      disabled: true,
      error: '',
    });
    await renderCard();
    await userEvent.click(screen.getByRole('button', { name: /check for updates/i }));

    expect(await screen.findByText(/turned off on this deployment/i)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /check for updates/i })).not.toBeInTheDocument();
  });
  it('does not tell a build made from source that it is on the latest release', async () => {
    // A build with no release number cannot be ordered against one: it may be
    // ahead of the latest release, behind it, or unrelated. Saying "this is the
    // latest release" would be a claim the server never made.
    checkForUpdate.mockResolvedValue({
      currentVersion: 'dev-1a2b3c4d5e6f',
      latestVersion: 'v0.1.0',
      releaseUrl: 'https://github.com/TikHub/Spinneret/releases/tag/v0.1.0',
      updateAvailable: false,
      currentIsRelease: false,
      disabled: false,
      error: '',
    });
    await renderCard();
    await userEvent.click(screen.getByRole('button', { name: /check for updates/i }));

    expect(await screen.findByText(/made from source/i)).toBeInTheDocument();
    expect(screen.queryByText('This is the latest release.')).not.toBeInTheDocument();
    // The release it could not compare against is still worth showing.
    expect(screen.getByText('v0.1.0')).toBeInTheDocument();
  });
});
