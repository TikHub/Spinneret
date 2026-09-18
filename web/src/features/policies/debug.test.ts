import { create } from '@bufbuild/protobuf';
import { describe, expect, it } from 'vitest';

import { CounterRequirementSchema } from '@/gen/spinneret/v1/policy_admin_pb';

import {
  buildBanCounts,
  buildCounts,
  buildDebugRequest,
  counterKey,
  countEntriesFor,
  emptyDebugForm,
  parseCounterKey,
  reportFieldsFromJson,
  type CountEntry,
} from './debug';

function entry(partial: Partial<CountEntry>): CountEntry {
  return {
    key: partial.key ?? 'e',
    subject: 'identity',
    outcome: 'captcha',
    window: '1h',
    value: '1',
    ...partial,
  };
}

describe('counter keys', () => {
  it('builds and parses "<subject>:<outcome>:<window>" keys', () => {
    expect(counterKey('identity', 'captcha', ' 1H ')).toBe('identity:captcha:1h');
    expect(counterKey('proxy', 'rate_limited', '10m')).toBe('proxy:rate_limited:10m');
    expect(parseCounterKey('account:banned:30d')).toEqual({
      subject: 'account',
      outcome: 'banned',
      window: '30d',
    });
    expect(parseCounterKey('node:captcha:1h')).toBeUndefined();
    expect(parseCounterKey('identity:captcha')).toBeUndefined();
  });

  it('builds the counts map and reports invalid, duplicate and non-numeric entries', () => {
    const { counts, errors } = buildCounts([
      entry({ key: 'a', value: '3' }),
      entry({ key: 'b', subject: 'proxy', outcome: 'rate_limited', window: '10m', value: '12' }),
      entry({ key: 'c', window: '1H', value: '4' }),
      entry({ key: 'd', outcome: 'nope' }),
      entry({ key: 'e', window: 'permanent' }),
      entry({ key: 'f', subject: 'account', value: '-1' }),
    ]);
    expect(counts).toEqual({ 'identity:captcha:1h': 3n, 'proxy:rate_limited:10m': 12n });
    expect(errors).toEqual({ c: 'duplicate', d: 'key', e: 'key', f: 'value' });
  });

  it('validates ban count windows', () => {
    const { banCounts, errors } = buildBanCounts([
      { key: 'a', window: '30d', value: '2' },
      { key: 'b', window: '', value: '1' },
      { key: 'c', window: '7d', value: 'x' },
    ]);
    expect(banCounts).toEqual({ '30d': 2n });
    expect(errors).toEqual({ b: 'key', c: 'value' });
  });

  it('adds entries for required counters without duplicates', () => {
    const requirements = [
      create(CounterRequirementSchema, { subject: 'identity', outcome: 'captcha', window: '24h' }),
      create(CounterRequirementSchema, { subject: 'identity', outcome: 'empty', window: '10m' }),
      create(CounterRequirementSchema, { subject: 'identity', outcome: 'empty', window: '10m' }),
    ];
    const added = countEntriesFor(requirements, [entry({ window: '24h' })]);
    expect(added.map((e) => counterKey(e.subject, e.outcome, e.window))).toEqual(['identity:empty:10m']);
    expect(added[0]?.value).toBe('1');
  });
});

describe('debug request', () => {
  it('requires a target and validates numeric fields', () => {
    const form = { ...emptyDebugForm(), report: { ...emptyDebugForm().report, httpStatus: '1000' } };
    const result = buildDebugRequest(form, 'default');
    expect(result.ok).toBe(false);
    if (result.ok) return;
    expect(result.fieldErrors).toMatchObject({
      site: 'required',
      client: 'required',
      endpointGroup: 'required',
      'report.httpStatus': 'httpStatus',
    });
  });

  it('builds the request with counters, target and draft policy', () => {
    const base = emptyDebugForm();
    const form = {
      ...base,
      site: 'shop',
      client: 'web',
      targetMode: 'uri' as const,
      uri: '/api/v1/search',
      useDraft: true,
      report: {
        ...base.report,
        method: 'post',
        httpStatus: '429',
        markers: ['captcha_page'],
        responseBytes: '512',
      },
      context: {
        ...base.context,
        identityState: 'pending',
        endpointStreak: '3',
        endpointCooldownRemaining: '5m',
      },
      counts: [entry({ value: '2' })],
      banCounts: [{ key: 'b', window: '7d', value: '1' }],
    };
    const result = buildDebugRequest(form, 'default', 'pol_1');
    expect(result.ok).toBe(true);
    if (!result.ok) return;
    expect(result.request).toMatchObject({
      namespace: 'default',
      site: 'shop',
      client: 'web',
      target: { case: 'uri', value: '/api/v1/search' },
      report: { method: 'POST', httpStatus: 429, markers: ['captcha_page'], responseBytes: 512n },
      identityState: 'pending',
      counts: { 'identity:captcha:1h': 2n },
      banCounts: { '7d': 1n },
      endpointStreak: 3,
      draftPolicyId: 'pol_1',
      endpointCooldownRemaining: '5m',
    });
    const withoutDraft = buildDebugRequest(
      { ...form, useDraft: false, targetMode: 'endpoint_group', endpointGroup: 'search' },
      'default',
      'pol_1',
    );
    expect(withoutDraft.ok && withoutDraft.request.draftPolicyId).toBe('');
    expect(withoutDraft.ok && withoutDraft.request.target).toEqual({
      case: 'endpointGroup',
      value: 'search',
    });
  });
});

describe('pasted reports', () => {
  it('reads snake_case, camelCase and batch reports', () => {
    const current = emptyDebugForm().report;
    expect(
      reportFieldsFromJson(
        '{"uri":"/api/search","http_status":429,"markers":["captcha_page"],"latency_ms":120,"response_bytes":"512","error_kind":""}',
        current,
      ),
    ).toEqual({
      uri: '/api/search',
      method: 'GET',
      httpStatus: '429',
      businessCode: '',
      errorKind: '',
      markers: ['captcha_page'],
      latencyMs: '120',
      responseBytes: '512',
      outcomeHint: '',
    });
    expect(
      reportFieldsFromJson('{"reports":[{"httpStatus":200,"businessCode":"10001"}]}', current),
    ).toMatchObject({
      httpStatus: '200',
      businessCode: '10001',
    });
    expect(reportFieldsFromJson('not json', current)).toBeUndefined();
    expect(reportFieldsFromJson('[1]', current)).toBeUndefined();
  });
});
