import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, type RenderResult } from '@testing-library/react';
import i18next from 'i18next';
import { type ReactElement } from 'react';
import { I18nextProvider, initReactI18next } from 'react-i18next';

import { TooltipProvider } from '@/components/ui/tooltip';
import common from '@/i18n/locales/en/common.json';
import policies from '@/i18n/locales/en/policies.json';

/** Test-only: renders with a fresh QueryClient, English translations and the tooltip provider. */
export async function renderWithProviders(ui: ReactElement): Promise<RenderResult> {
  const i18n = i18next.createInstance();
  await i18n.use(initReactI18next).init({
    lng: 'en',
    resources: { en: { common, policies } },
    ns: ['common', 'policies'],
    defaultNS: 'common',
    fallbackNS: 'common',
    interpolation: { escapeValue: false },
    react: { useSuspense: false },
  });
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <I18nextProvider i18n={i18n}>
        <TooltipProvider>{ui}</TooltipProvider>
      </I18nextProvider>
    </QueryClientProvider>,
  );
}
