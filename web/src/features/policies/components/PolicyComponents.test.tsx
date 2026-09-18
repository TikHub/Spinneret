import { create } from '@bufbuild/protobuf';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import {
  CounterRequirementSchema,
  DebugReportResponseSchema,
  ListPoliciesResponseSchema,
  PlannedActionSchema,
  PolicySchema,
} from '@/gen/spinneret/v1/policy_admin_pb';

import { policyTemplate } from '../templates';

const calls = vi.hoisted(() => ({ debugReport: 0 }));

vi.mock('@/lib/clients', async () => {
  const { createClient, createRouterTransport } = await import('@connectrpc/connect');
  const { PolicyAdminService } = await import('@/gen/spinneret/v1/policy_admin_pb');
  const { SiteAdminService, ListSitesResponseSchema } = await import('@/gen/spinneret/v1/site_admin_pb');
  const transport = createRouterTransport(({ service }) => {
    service(PolicyAdminService, {
      listPolicies: () => create(ListPoliciesResponseSchema, { policies: [] }),
      debugReport: () => {
        calls.debugReport += 1;
        return create(DebugReportResponseSchema, {});
      },
    });
    service(SiteAdminService, {
      listSites: () => create(ListSitesResponseSchema, { sites: [] }),
    });
  });
  return {
    policyClient: createClient(PolicyAdminService, transport),
    siteClient: createClient(SiteAdminService, transport),
  };
});

vi.mock('@/app/auth/AuthContext', async () => {
  const { keepPreviousInScope, scopedKey } = await import('@/lib/queryKeys');
  const auth = { tenantId: 'ten_1', namespaceName: 'default', can: () => true, canInTenant: () => true };
  return {
    useAuth: () => auth,
    useScopedQueryKey:
      () =>
      (domain: string, ...parts: unknown[]) =>
        scopedKey(domain as never, 'ten_1', 'default', ...parts),
    useScopedPlaceholder: () => keepPreviousInScope('ten_1', 'default'),
  };
});

const { PolicyBuilder } = await import('./editor/PolicyBuilder');
const { DebuggerPanel } = await import('./debugger/DebuggerPanel');
const { DebugResult } = await import('./debugger/DebugResult');
const { renderWithProviders } = await import('../testing');

describe('PolicyBuilder', () => {
  it('loads signal rules from YAML and regenerates YAML on edits', async () => {
    const user = userEvent.setup();
    const onTextChange = vi.fn();
    await renderWithProviders(
      <PolicyBuilder
        kind="signal"
        text={policyTemplate('signal', 'sig')}
        onTextChange={onTextChange}
        onOpenYaml={() => undefined}
        onResetTemplate={() => undefined}
      />,
    );
    expect(screen.getByText('proxy-error')).toBeInTheDocument();
    expect(screen.getByText('{ http_status: { gte: 200, lt: 300 } }')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Move rule 2 up' }));
    const moved = onTextChange.mock.lastCall?.[0] as string;
    expect(moved.indexOf('name: network-error')).toBeLessThan(moved.indexOf('name: proxy-error'));
    expect(moved).not.toContain('#');

    await user.click(screen.getByRole('button', { name: 'Add rule' }));
    const added = onTextChange.mock.lastCall?.[0] as string;
    expect(added.endsWith('  - when: {}\n    outcome: success\n')).toBe(true);
  });

  it('edits rotation fields in form mode', async () => {
    const user = userEvent.setup();
    const onTextChange = vi.fn();
    await renderWithProviders(
      <PolicyBuilder
        kind="rotation"
        text="name: rot\n"
        onTextChange={onTextChange}
        onOpenYaml={() => undefined}
        onResetTemplate={() => undefined}
      />,
    );
    await user.type(screen.getByLabelText('Candidate sample'), '8');
    expect(onTextChange.mock.lastCall?.[0]).toContain('  candidate_sample: 8\n');
  });

  it('falls back to the YAML editor for unsupported YAML', async () => {
    const user = userEvent.setup();
    const onOpenYaml = vi.fn();
    await renderWithProviders(
      <PolicyBuilder
        kind="breaker"
        text={'name: b\nwindow: &w 60s\n'}
        onTextChange={() => undefined}
        onOpenYaml={onOpenYaml}
        onResetTemplate={() => undefined}
      />,
    );
    expect(screen.getByText('This YAML cannot be edited in the form')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Open YAML editor' }));
    expect(onOpenYaml).toHaveBeenCalled();
  });
});

describe('DebuggerPanel', () => {
  beforeEach(() => {
    calls.debugReport = 0;
  });

  it('validates the form before calling DebugReport and fills fields from a pasted report', async () => {
    const user = userEvent.setup();
    const draft = create(PolicySchema, { id: 'pol_1', name: 'sig', kind: 'signal', hasDraft: true });
    await renderWithProviders(<DebuggerPanel draftPolicy={draft} />);

    await user.click(screen.getByRole('button', { name: 'Run debugger' }));
    expect(await screen.findByText('Fix the highlighted fields.')).toBeInTheDocument();
    expect(calls.debugReport).toBe(0);
    expect(
      screen.getByRole('checkbox', { name: 'Use the draft of sig instead of its published version' }),
    ).toBeChecked();

    await user.click(screen.getByRole('button', { name: 'Paste report' }));
    const dialog = await screen.findByRole('dialog');
    await user.click(within(dialog).getByRole('textbox'));
    await user.paste('{"uri":"/api/search","http_status":429,"latency_ms":80}');
    await user.click(within(dialog).getByRole('button', { name: 'Use report' }));
    await waitFor(() => expect(screen.getByLabelText(/Report URI/)).toHaveValue('/api/search'));
    expect(screen.getByLabelText('HTTP status')).toHaveValue('429');
  });
});

describe('DebugResult', () => {
  it('shows the classification, planned actions and missing counters', async () => {
    const user = userEvent.setup();
    const onAddCounters = vi.fn();
    const result = create(DebugReportResponseSchema, {
      outcome: 'captcha',
      blame: 'identity',
      matchedRuleIndex: 2,
      matchedRuleName: 'captcha',
      mode: 'shadow',
      actions: [
        create(PlannedActionSchema, {
          action: 'ban',
          scope: 'identity',
          duration: '12h',
          severity: 4,
          ruleName: 'captcha-ban',
          source: 'rule',
          ruleIndex: 3,
        }),
      ],
      counters: [
        create(CounterRequirementSchema, { subject: 'identity', outcome: 'captcha', window: '24h' }),
        create(CounterRequirementSchema, { subject: 'identity', outcome: 'captcha', window: '1h' }),
      ],
    });
    await renderWithProviders(
      <DebugResult
        result={result}
        counts={[{ key: 'c1', subject: 'identity', outcome: 'captcha', window: '24h', value: '3' }]}
        onAddCounters={onAddCounters}
      />,
    );
    expect(screen.getByText('#3')).toBeInTheDocument();
    expect(screen.getByText('Shadow')).toBeInTheDocument();
    const table = screen.getByRole('table');
    expect(within(table).getByText('#4 captcha-ban')).toBeInTheDocument();
    expect(within(table).getByText('12h')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Add 1 missing counter' }));
    expect(onAddCounters).toHaveBeenCalled();
  });
});
