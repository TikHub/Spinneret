import { create } from '@bufbuild/protobuf';
import { timestampFromMs } from '@bufbuild/protobuf/wkt';
import { screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';

import { type CursorPagination } from '@/components/data-table';
import {
  BreakerStatusSchema,
  ProbeMetricsSchema,
  WindowMetricsSchema,
} from '@/gen/spinneret/v1/breaker_admin_pb';

const auth = vi.hoisted(() => ({ allowedSites: new Set(['sit_1']) }));

vi.mock('@/app/auth/AuthContext', () => ({
  useAuth: () => ({
    can: (_permission: string, site?: string) => site !== undefined && auth.allowedSites.has(site),
    canInTenant: () => false,
  }),
}));

const { BreakerTable } = await import('./BreakerTable');
const { renderWithProviders } = await import('../testing');

const pager: CursorPagination = {
  pageToken: '',
  pageSize: 50,
  pageIndex: 0,
  canPrevious: false,
  next: () => undefined,
  previous: () => undefined,
  first: () => undefined,
  setPageSize: () => undefined,
};

const breakers = [
  create(BreakerStatusSchema, {
    site: 'shop',
    siteId: 'sit_1',
    client: 'web',
    endpointGroup: 'search',
    endpointGroupId: 'eg_search',
    state: 'open',
    openUntil: timestampFromMs(Date.now() + 5 * 60_000),
    consecutiveOpens: 2,
    reason: 'risk ratio 0.52 >= 0.4',
    window: create(WindowMetricsSchema, {
      total: 120,
      success: 40,
      risk: 62,
      captchaIdentities: 11,
      riskRatio: 0.52,
      successRatio: 0.33,
    }),
  }),
  create(BreakerStatusSchema, {
    site: 'shop',
    siteId: 'sit_1',
    client: 'web',
    endpointGroup: 'detail',
    endpointGroupId: 'eg_detail',
    state: 'open',
    manual: true,
    reason: 'maintenance',
  }),
  create(BreakerStatusSchema, {
    site: 'shop',
    siteId: 'sit_1',
    client: 'app',
    endpointGroup: 'feed',
    endpointGroupId: 'eg_feed',
    state: 'half_open',
    probe: create(ProbeMetricsSchema, { samples: 4, successes: 3, issued: 2 }),
  }),
  create(BreakerStatusSchema, {
    site: 'market',
    siteId: 'sit_2',
    client: 'web',
    endpointGroup: '_default',
    endpointGroupId: 'eg_default',
    state: 'closed',
  }),
];

function row(name: string): HTMLElement {
  const cell = screen.getByText(name);
  const tr = cell.closest('tr');
  if (!tr) throw new Error(`row ${name} not found`);
  return tr;
}

describe('BreakerTable', () => {
  it('shows countdowns, indefinite opens, probe metrics and window ratios', async () => {
    await renderWithProviders(
      <BreakerTable
        breakers={breakers}
        isLoading={false}
        isFetching={false}
        error={undefined}
        onRetry={() => undefined}
        pagination={{ pager, total: breakers.length }}
        onOpen={() => undefined}
        onClose={() => undefined}
      />,
    );
    expect(within(row('search')).getByText(/^[45]m \d{2}s$/)).toBeInTheDocument();
    expect(within(row('search')).getByRole('meter', { name: 'Risk' })).toHaveAttribute('aria-valuenow', '52');
    expect(within(row('detail')).getByText('Indefinitely')).toBeInTheDocument();
    expect(within(row('detail')).getByText('Manual')).toBeInTheDocument();
    expect(within(row('feed')).getByText('3/4 ok · 2 issued')).toBeInTheDocument();
    expect(within(row('_default')).getByText('No reports in the window')).toBeInTheDocument();
  });

  it('offers open or close by state and disables actions without breaker:operate on the site', async () => {
    const user = userEvent.setup();
    const onOpen = vi.fn();
    const onClose = vi.fn();
    await renderWithProviders(
      <BreakerTable
        breakers={breakers}
        isLoading={false}
        isFetching={false}
        error={undefined}
        onRetry={() => undefined}
        pagination={{ pager }}
        onOpen={onOpen}
        onClose={onClose}
      />,
    );
    expect(within(row('search')).queryByRole('button', { name: 'Open' })).not.toBeInTheDocument();
    await user.click(within(row('search')).getByRole('button', { name: 'Close' }));
    expect(onClose).toHaveBeenCalledWith(breakers[0]);

    const feed = row('feed');
    expect(within(feed).getByRole('button', { name: 'Open' })).toBeEnabled();
    expect(within(feed).getByRole('button', { name: 'Close' })).toBeEnabled();

    expect(within(row('_default')).getByRole('button', { name: 'Open' })).toBeDisabled();
    expect(within(row('_default')).queryByRole('button', { name: 'Close' })).not.toBeInTheDocument();
    expect(onOpen).not.toHaveBeenCalled();
  });
});
