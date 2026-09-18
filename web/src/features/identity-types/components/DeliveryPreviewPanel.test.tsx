import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import i18next from 'i18next';
import { useState } from 'react';
import { I18nextProvider, initReactI18next } from 'react-i18next';
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';

import { TooltipProvider } from '@/components/ui/tooltip';
import {
  type PreviewDeliveryRequest,
  type PreviewDeliveryResponse,
} from '@/gen/spinneret/v1/identity_admin_pb';
import enCommon from '@/i18n/locales/en/common.json';
import enIdentityTypes from '@/i18n/locales/en/identity-types.json';

const handlers = vi.hoisted(() => ({ previewDelivery: vi.fn() }));

vi.mock('@/app/auth/AuthContext', () => ({
  useAuth: () => ({ namespaceName: 'default', tenantId: 'ten_1', can: () => true }),
}));

// Monaco does not run in jsdom; a textarea stands in for the editor.
vi.mock('@/components/editor/CodeEditor', () => ({
  CodeEditor: ({
    value,
    onChange,
    'aria-label': label,
  }: {
    value: string;
    onChange?: (v: string) => void;
    'aria-label'?: string;
  }) => <textarea aria-label={label} value={value} onChange={(e) => onChange?.(e.target.value)} />,
}));

vi.mock('@/lib/clients', async () => {
  const { createClient, createRouterTransport } = await import('@connectrpc/connect');
  const { IdentityAdminService } = await import('@/gen/spinneret/v1/identity_admin_pb');
  const transport = createRouterTransport(({ service }) => {
    service(IdentityAdminService, {
      previewDelivery: (req: PreviewDeliveryRequest) => handlers.previewDelivery(req),
    });
  });
  return { identityClient: createClient(IdentityAdminService, transport) };
});

const { DeliveryPreviewPanel } = await import('./DeliveryPreviewPanel');

beforeAll(async () => {
  await i18next.use(initReactI18next).init({
    lng: 'en',
    resources: { en: { common: enCommon, 'identity-types': enIdentityTypes } },
    defaultNS: 'common',
    fallbackNS: 'common',
    interpolation: { escapeValue: false },
    react: { useSuspense: false },
  });
});

afterEach(() => {
  handlers.previewDelivery.mockReset();
});

function Harness({ specYaml }: { specYaml: string }) {
  const [payload, setPayload] = useState('{"cookies": "sessionid=abc; csrf_token=x"}');
  const [result, setResult] = useState<PreviewDeliveryResponse>();
  return (
    <DeliveryPreviewPanel
      site="shop"
      source={{ case: 'specYaml', value: specYaml }}
      payload={payload}
      onPayloadChange={setPayload}
      result={result}
      onResult={setResult}
      editorPath="test.json"
    />
  );
}

function renderPanel(specYaml = 'name: t') {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <I18nextProvider i18n={i18next}>
      <QueryClientProvider client={client}>
        <TooltipProvider>
          <Harness specYaml={specYaml} />
        </TooltipProvider>
      </QueryClientProvider>
    </I18nextProvider>,
  );
}

describe('DeliveryPreviewPanel', () => {
  it('renders the delivered credential segments and the normalized payload', async () => {
    handlers.previewDelivery.mockResolvedValue({
      credential: {
        cookies: { sessionid: 'abc', csrf_token: 'x' },
        cookieHeader: 'sessionid=abc; csrf_token=x',
        headers: { 'User-Agent': 'Mozilla' },
      },
      normalizedPayload: { cookies: { sessionid: 'abc', csrf_token: 'x' } },
      errors: [],
    });
    renderPanel();
    fireEvent.click(screen.getByRole('button', { name: 'Render' }));

    await waitFor(() => expect(screen.getByText('sessionid=abc; csrf_token=x')).toBeInTheDocument());
    const request = handlers.previewDelivery.mock.calls[0]?.[0] as PreviewDeliveryRequest;
    expect(request.namespace).toBe('default');
    expect(request.site).toBe('shop');
    expect(request.source).toEqual({ case: 'specYaml', value: 'name: t' });
    expect(request.payloadJson).toBe('{"cookies": "sessionid=abc; csrf_token=x"}');
    expect(screen.getByText('User-Agent')).toBeInTheDocument();
    expect(screen.getByText('Normalized payload')).toBeInTheDocument();
    expect(screen.getByText('Unused segments:')).toBeInTheDocument();
    expect(screen.getByText('query')).toBeInTheDocument();
  });

  it('shows validation errors returned by the server', async () => {
    handlers.previewDelivery.mockResolvedValue({ errors: ['cookies: required field is missing'] });
    renderPanel();
    fireEvent.click(screen.getByRole('button', { name: 'Render' }));
    await waitFor(() => expect(screen.getByText('cookies: required field is missing')).toBeInTheDocument());
  });

  it('disables rendering for a payload that is not a JSON object', () => {
    renderPanel();
    fireEvent.change(screen.getByLabelText('Sample payload'), { target: { value: '[1, 2]' } });
    expect(screen.getByRole('button', { name: 'Render' })).toBeDisabled();
    expect(screen.getByText('The payload must be a JSON object.')).toBeInTheDocument();
  });
});
