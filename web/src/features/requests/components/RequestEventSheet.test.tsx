import { create } from '@bufbuild/protobuf';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import i18next from 'i18next';
import { I18nextProvider, initReactI18next } from 'react-i18next';
import { beforeAll, describe, expect, it, vi } from 'vitest';

import { TooltipProvider } from '@/components/ui/tooltip';
import { RequestEventSchema } from '@/gen/spinneret/v1/dashboard_pb';
import enCommon from '@/i18n/locales/en/common.json';

// Without identity:read the identity field renders a plain ID instead of a
// router Link, so the sheet can be rendered without a RouterProvider.
vi.mock('@/app/auth/AuthContext', async () =>
  (await import('../shared/testing')).authModuleMock(() => false),
);

const { RequestEventSheet } = await import('./RequestEventSheet');

const EVENT = create(RequestEventSchema, {
  method: 'GET',
  uri: '/api/v3/catalog/items?category=shoes&page=42',
  outcome: 'success',
  site: 'shop',
  client: 'web',
  endpointGroup: 'catalog',
  identityId: 'idt_01a0b0ff38247591ab1f2e5350fa5bb7',
  proxyId: 'prx_01a0b0ff38247591ab1f2e5350fa5bb8',
  tokenId: 'tok_01a0b0ff38247591ab1f2e5350fa5bb9',
  leaseId: 'lse_01a0b0ff38247591ab1f2e5350fa5bba',
  reportId: 'rpt_01a0b0ff38247591ab1f2e5350fa5bbb',
});

beforeAll(async () => {
  await i18next.use(initReactI18next).init({
    lng: 'en',
    resources: { en: { common: enCommon } },
    defaultNS: 'common',
    fallbackNS: 'common',
    interpolation: { escapeValue: false },
    react: { useSuspense: false },
  });
});

function renderSheet(onOpenChange = vi.fn()) {
  render(
    <I18nextProvider i18n={i18next}>
      <TooltipProvider>
        <RequestEventSheet event={EVENT} onOpenChange={onOpenChange} />
      </TooltipProvider>
    </I18nextProvider>,
  );
  return onOpenChange;
}

describe('RequestEventSheet', () => {
  it('keeps the initial focus on the panel instead of the first copy button', async () => {
    renderSheet();

    const panel = await screen.findByRole('dialog');
    // Focusing a copy button would open its tooltip over the neighbouring
    // fields and swallow the first Escape press.
    await waitFor(() => expect(panel).toHaveFocus());
    expect(document.activeElement?.tagName).not.toBe('BUTTON');
  });

  it('closes on the first Escape press', async () => {
    const user = userEvent.setup();
    const onOpenChange = renderSheet();

    await screen.findByRole('dialog');
    await user.keyboard('{Escape}');

    expect(onOpenChange).toHaveBeenCalledWith(false);
  });

  it('closes when the close button is activated', async () => {
    const user = userEvent.setup();
    const onOpenChange = renderSheet();

    const panel = await screen.findByRole('dialog');
    await user.click(await screen.findByRole('button', { name: 'Close' }));

    expect(onOpenChange).toHaveBeenCalledWith(false);
    expect(panel).toBeInTheDocument();
  });

  it('scrolls the fields in their own box so the header and close button stay put', async () => {
    renderSheet();

    const panel = await screen.findByRole('dialog');
    // The panel must not be the scroll container: the close button is
    // positioned against it and would scroll out of reach.
    expect(panel.className).toContain('overflow-y-hidden');
    const list = panel.querySelector('dl');
    expect(list).not.toBeNull();
    const scroller = list?.closest('.overflow-y-auto');
    expect(scroller).not.toBeNull();
    expect(panel.contains(scroller as Node)).toBe(true);
    // No sticky header: it would cover the panel's own close button.
    expect(panel.querySelector('.sticky')).toBeNull();
  });
});
