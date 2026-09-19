import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen, type RenderResult } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import i18next from 'i18next';
import { I18nextProvider, initReactI18next } from 'react-i18next';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { TooltipProvider } from '@/components/ui/tooltip';
import { SettingOrigin } from '@/gen/spinneret/v1/system_pb';
import common from '@/i18n/locales/en/common.json';

const listSettings = vi.hoisted(() => vi.fn());
const updateSettings = vi.hoisted(() => vi.fn());

vi.mock('@/lib/clients', () => ({ systemClient: { listSettings, updateSettings } }));

const { RetentionCard } = await import('./RetentionCard');

function setting(over: Partial<Record<string, unknown>> = {}) {
  return {
    key: 'retention.audit',
    value: '365d',
    defaultValue: '365d',
    origin: SettingOrigin.DEFAULT,
    envVar: 'SPINNERET_RETENTION_AUDIT',
    unit: 'duration',
    minimum: '1d',
    maximum: '3650d',
    ...over,
  };
}

async function renderCard(canEdit = true): Promise<RenderResult> {
  const i18n = i18next.createInstance();
  await i18n.use(initReactI18next).init({
    lng: 'en',
    resources: { en: { common } },
    ns: ['common'],
    defaultNS: 'common',
    interpolation: { escapeValue: false },
    react: { useSuspense: false },
  });
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <I18nextProvider i18n={i18n}>
        {/* The app mounts one in providers.tsx; the card uses tooltips for the
            lock badge and the reset button. */}
        <TooltipProvider>
          <RetentionCard canEdit={canEdit} />
        </TooltipProvider>
      </I18nextProvider>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  listSettings.mockReset();
  updateSettings.mockReset();
});

describe('RetentionCard', () => {
  it('sends only the fields that were actually edited', async () => {
    listSettings.mockResolvedValue({
      canEdit: true,
      settings: [setting(), setting({ key: 'retention.risk_events', value: '30d', defaultValue: '30d' })],
    });
    updateSettings.mockResolvedValue({ settings: [] });
    await renderCard();

    const audit = await screen.findByLabelText('Audit log');
    await userEvent.clear(audit);
    await userEvent.type(audit, '90d');
    await userEvent.click(screen.getByRole('button', { name: 'Save' }));

    expect(updateSettings).toHaveBeenCalledWith({ values: { 'retention.audit': '90d' } });
  });

  it('locks a setting the environment pins and names the variable', async () => {
    listSettings.mockResolvedValue({
      canEdit: true,
      settings: [setting({ value: '48h', origin: SettingOrigin.ENVIRONMENT })],
    });
    await renderCard();

    expect(await screen.findByText('From the environment')).toBeInTheDocument();
    expect(screen.getByLabelText('Audit log')).toBeDisabled();
  });

  it('is read-only for a user who is not a platform administrator', async () => {
    listSettings.mockResolvedValue({ canEdit: false, settings: [setting()] });
    await renderCard(false);

    expect(await screen.findByText(/only a platform administrator/i)).toBeInTheDocument();
    expect(screen.getByLabelText('Audit log')).toBeDisabled();
    expect(screen.queryByRole('button', { name: 'Save' })).not.toBeInTheDocument();
  });

  it('offers a reset only for a setting that was changed away from its default', async () => {
    listSettings.mockResolvedValue({
      canEdit: true,
      settings: [
        setting({ value: '90d', origin: SettingOrigin.DATABASE }),
        setting({ key: 'retention.risk_events', value: '30d', defaultValue: '30d' }),
      ],
    });
    updateSettings.mockResolvedValue({ settings: [] });
    await renderCard();

    expect(await screen.findByText('Changed')).toBeInTheDocument();
    const resets = screen.getAllByRole('button', { name: /reset to the default/i });
    expect(resets).toHaveLength(1);
    const [reset] = resets;
    expect(reset).toBeDefined();

    // A reset is an empty value, which is what tells the server to forget its row.
    await userEvent.click(reset!);
    expect(updateSettings).toHaveBeenCalledWith({ values: { 'retention.audit': '' } });
  });

  it('shows the reason the server refused a value', async () => {
    listSettings.mockResolvedValue({ canEdit: true, settings: [setting()] });
    updateSettings.mockRejectedValue(new Error('retention.audit: 1h is outside 1d-3650d'));
    await renderCard();

    const audit = await screen.findByLabelText('Audit log');
    await userEvent.clear(audit);
    await userEvent.type(audit, '1h');
    await userEvent.click(screen.getByRole('button', { name: 'Save' }));

    expect(await screen.findByText(/is outside 1d-3650d/)).toBeInTheDocument();
  });
});
