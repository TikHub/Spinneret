import { Code, ConnectError } from '@connectrpc/connect';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import i18next from 'i18next';
import { I18nextProvider, initReactI18next } from 'react-i18next';
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';

import { TooltipProvider } from '@/components/ui/tooltip';
import { type ReplaceURIRulesRequest } from '@/gen/spinneret/v1/site_admin_pb';
import enCommon from '@/i18n/locales/en/common.json';
import enSites from '@/i18n/locales/en/sites.json';

const STORED_RULES = [
  { id: 'uri_c', kind: 'exact', pattern: '/c', position: 2 },
  { id: 'uri_a', kind: 'prefix', pattern: '/a/', position: 0 },
  { id: 'uri_b', kind: 'regex', pattern: '^/b', position: 1 },
];

const handlers = vi.hoisted(() => ({
  listURIRules: vi.fn(),
  replaceURIRules: vi.fn(),
}));

vi.mock('@/app/auth/AuthContext', () => ({
  useAuth: () => ({
    namespaceName: 'default',
    tenantId: 'ten_1',
    isPlatformAdmin: false,
    namespace: undefined,
    can: () => true,
    canInTenant: () => true,
  }),
  useScopedQueryKey:
    () =>
    (domain: string, ...parts: unknown[]) => [domain, 'ten_1', 'default', ...parts],
  useScopedPlaceholder: () => undefined,
}));

// The site service is served by an in-memory Connect router (no backend).
vi.mock('@/lib/clients', async () => {
  const { createClient, createRouterTransport } = await import('@connectrpc/connect');
  const { SiteAdminService } = await import('@/gen/spinneret/v1/site_admin_pb');
  const transport = createRouterTransport(({ service }) => {
    service(SiteAdminService, {
      listURIRules: () => handlers.listURIRules(),
      replaceURIRules: (req: ReplaceURIRulesRequest) => handlers.replaceURIRules(req),
    });
  });
  return { siteClient: createClient(SiteAdminService, transport), breakerClient: {} };
});

const { URIRulesSheet } = await import('./URIRulesSheet');

const target = { id: 'eg_1', name: 'search', client: 'web', site: 'shop', siteId: 'sit_1' };

beforeAll(async () => {
  await i18next.use(initReactI18next).init({
    lng: 'en',
    resources: { en: { common: enCommon, sites: enSites } },
    defaultNS: 'common',
    fallbackNS: 'common',
    interpolation: { escapeValue: false },
    react: { useSuspense: false },
  });
});

beforeEach(() => {
  handlers.listURIRules.mockImplementation(() => ({ rules: STORED_RULES, nextPageToken: '', total: 3 }));
});

afterEach(() => {
  handlers.listURIRules.mockReset();
  handlers.replaceURIRules.mockReset();
});

function renderSheet(client = new QueryClient({ defaultOptions: { queries: { retry: false } } })) {
  return render(
    <I18nextProvider i18n={i18next}>
      <QueryClientProvider client={client}>
        <TooltipProvider>
          <URIRulesSheet target={target} open onOpenChange={() => undefined} />
        </TooltipProvider>
      </QueryClientProvider>
    </I18nextProvider>,
  );
}

const patterns = () =>
  screen
    .getAllByRole('textbox', { name: /^Pattern of rule/ })
    .map((input) => (input as HTMLInputElement).value);

describe('URIRulesSheet', () => {
  it('loads rules in position order, reorders them and saves positions from list order', async () => {
    handlers.replaceURIRules.mockImplementation((req: ReplaceURIRulesRequest) => ({
      rules: req.rules.map((rule, position) => ({ ...rule, id: rule.id || `uri_new_${position}`, position })),
    }));
    renderSheet();

    await waitFor(() => expect(patterns()).toEqual(['/a/', '^/b', '/c']));
    const save = screen.getByRole('button', { name: 'Save' });
    expect(save).toBeDisabled();

    fireEvent.click(screen.getByRole('button', { name: 'Move rule 3 up' }));
    expect(patterns()).toEqual(['/a/', '/c', '^/b']);
    expect(screen.getByText('Unsaved changes')).toBeInTheDocument();

    // A pattern without a leading slash is a blocking client-side error.
    fireEvent.change(screen.getByRole('textbox', { name: 'Pattern of rule 1' }), {
      target: { value: 'api/' },
    });
    expect(screen.getByText('The pattern must start with "/".')).toBeInTheDocument();
    expect(save).toBeDisabled();

    fireEvent.change(screen.getByRole('textbox', { name: 'Pattern of rule 1' }), {
      target: { value: '/api/' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Add rule' }));
    fireEvent.change(screen.getByRole('textbox', { name: 'Pattern of rule 4' }), {
      target: { value: '/d/' },
    });
    await waitFor(() => expect(save).toBeEnabled());
    fireEvent.click(save);

    await waitFor(() => expect(handlers.replaceURIRules).toHaveBeenCalledTimes(1));
    const request = handlers.replaceURIRules.mock.calls[0]?.[0] as ReplaceURIRulesRequest;
    expect(request.endpointGroupId).toBe('eg_1');
    expect(request.rules.map((r) => [r.id, r.kind, r.pattern, r.position])).toEqual([
      ['uri_a', 'prefix', '/api/', 0],
      ['uri_c', 'exact', '/c', 1],
      ['uri_b', 'regex', '^/b', 2],
      ['', 'prefix', '/d/', 3],
    ]);
    await waitFor(() => expect(screen.queryByText('Unsaved changes')).not.toBeInTheDocument());
  });

  it('starts a reopened editor from freshly loaded rules, not from a cached list', async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const first = renderSheet(client);
    await waitFor(() => expect(patterns()).toEqual(['/a/', '^/b', '/c']));
    first.unmount();

    // Another operator replaces the rules while the editor is closed.
    handlers.listURIRules.mockImplementation(() => ({
      rules: [{ id: 'uri_z', kind: 'prefix', pattern: '/z/', position: 0 }],
      nextPageToken: '',
      total: 1,
    }));
    renderSheet(client);
    await waitFor(() => expect(patterns()).toEqual(['/z/']));
    expect(screen.queryByText('Unsaved changes')).not.toBeInTheDocument();
  });

  it('makes a row draggable only while its handle is pressed', async () => {
    renderSheet();
    await waitFor(() => expect(patterns()).toEqual(['/a/', '^/b', '/c']));
    const handle = screen.getByRole('button', { name: /^Reorder rule 1/ });
    const item = handle.closest('li[data-rule-key]') as HTMLElement;
    expect(item).toHaveAttribute('draggable', 'false');

    fireEvent.pointerDown(handle);
    expect(item).toHaveAttribute('draggable', 'true');
    // Released elsewhere (not on the handle): the row must not stay draggable.
    fireEvent.pointerUp(document.body);
    await waitFor(() => expect(item).toHaveAttribute('draggable', 'false'));
  });

  it('shows a server validation error on the rule it refers to', async () => {
    handlers.replaceURIRules.mockImplementation(() => {
      throw new ConnectError('rules[1]: invalid regex: missing closing )', Code.InvalidArgument);
    });
    renderSheet();

    await waitFor(() => expect(patterns()).toEqual(['/a/', '^/b', '/c']));
    fireEvent.change(screen.getByRole('textbox', { name: 'Pattern of rule 2' }), {
      target: { value: '^/(b' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));

    const row = (position: number) =>
      screen
        .getByRole('textbox', { name: `Pattern of rule ${position}` })
        .closest('li[data-rule-key]') as HTMLElement;
    await waitFor(() =>
      expect(within(row(2)).getByRole('alert')).toHaveTextContent('rules[1]: invalid regex'),
    );
    expect(within(row(1)).queryByRole('alert')).not.toBeInTheDocument();

    // The error follows its rule when the list is reordered and clears once the rule is edited.
    fireEvent.click(screen.getByRole('button', { name: 'Move rule 2 up' }));
    await waitFor(() =>
      expect(within(row(1)).getByRole('alert')).toHaveTextContent('rules[1]: invalid regex'),
    );
    expect(within(row(2)).queryByRole('alert')).not.toBeInTheDocument();
    fireEvent.change(screen.getByRole('textbox', { name: 'Pattern of rule 1' }), {
      target: { value: '^/b' },
    });
    expect(within(row(1)).queryByRole('alert')).not.toBeInTheDocument();
  });
});
