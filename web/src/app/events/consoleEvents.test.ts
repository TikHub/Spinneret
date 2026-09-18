import { QueryClient } from '@tanstack/react-query';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { parseSseData } from '@/lib/sse';

import {
  flushInvalidations,
  INVALIDATION_COALESCE_MS,
  invalidateForEvent,
  invalidationDomains,
  parseConsoleEvent,
  toastForEvent,
} from './consoleEvents';

const t = (key: string, options?: Record<string, unknown>) => {
  const { defaultValue: _defaultValue, ...rest } = options ?? {};
  return Object.keys(rest).length > 0 ? `${key}${JSON.stringify(rest)}` : key;
};

/** Builds an SSE message the way the stream sends it: `event: <type>` + `data: <json>`. */
function sse(type: string, envelope: Record<string, unknown>) {
  return { type, lastEventId: '', data: parseSseData(JSON.stringify(envelope)) };
}

/*
 * Payloads exactly as the server marshals them (internal/server/sse.go writes
 * `event: <type>` and `data: <events.Event JSON>`):
 * - breaker.transition: breaker.TransitionData (internal/breaker/transition.go, admin.go siteSwitchData)
 * - identity.state / proxy.state: action.StateEventData (internal/action/events.go)
 * - alert: notify alertEventData (internal/notify/emit.go)
 * - config.published: configcenter.PublishedEventData (internal/configcenter/events.go)
 * - policy.published: policysvc.PublishedEvent (internal/policysvc/service.go)
 */
const BREAKER_TRANSITION = {
  namespace: 'default',
  site: 'shop',
  site_id: 'sit_a',
  client: 'web',
  endpoint_group: 'search',
  endpoint_group_id: 'eg_1',
  from: 'closed',
  to: 'open',
  trigger: 'auto',
  reason: 'risk_ratio',
  open_until: '2026-09-17T10:01:00Z',
  consecutive_opens: 1,
  manual: false,
  actor: '',
  metrics: {
    total: 40,
    success: 20,
    risk: 20,
    captcha_identities: 3,
    risk_ratio: 0.5,
    success_ratio: 0.5,
    probe_samples: 0,
    probe_successes: 0,
  },
};

function siteSwitch(paused: boolean, reason = '') {
  return {
    namespace: 'default',
    site: 'shop',
    site_id: 'sit_a',
    client: '',
    endpoint_group: '',
    endpoint_group_id: '',
    from: paused ? 'running' : 'paused',
    to: paused ? 'paused' : 'running',
    trigger: 'site_switch',
    reason,
    open_until: null,
    consecutive_opens: 0,
    manual: true,
    actor: 'user:usr_1',
    metrics: {
      total: 0,
      success: 0,
      risk: 0,
      captcha_identities: 0,
      risk_ratio: 0,
      success_ratio: 0,
      probe_samples: 0,
      probe_successes: 0,
    },
  };
}

const IDENTITY_STATE = {
  subject_kind: 'identity',
  subject_id: 'idn_1',
  site_id: 'sit_a',
  from: 'active',
  to: 'banned',
  action: 'ban',
  scope: 'site',
  until: '2026-09-17T11:00:00Z',
  reason: 'risk',
  actor: 'user:usr_1',
};

const PROXY_STATE = {
  subject_kind: 'proxy',
  subject_id: 'prx_1',
  site_id: '',
  from: 'active',
  to: 'dead',
  action: 'kill',
  until: null,
  reason: 'health_check',
};

const ALERT = {
  id: 'alr_1',
  kind: 'ban_spike',
  severity: 'warning',
  title: 'Ban spike on shop',
  message: '12 bans in 5 minutes',
  namespace_id: 'ns_1',
  site_id: 'sit_a',
  created_at: '2026-09-17T10:00:00Z',
};

const CONFIG_PUBLISHED = {
  item_id: 'cfg_1',
  group: 'crawler',
  key: 'app.yaml',
  version: 3,
  source_version: 1,
  actor: 'user:usr_1',
};

const POLICY_PUBLISHED = {
  policy_id: 'pol_1',
  name: 'default',
  kind: 'signal',
  version: 2,
  action: 'publish',
  site_id: 'sit_a',
  actor: 'user:usr_1',
};

describe('parseConsoleEvent', () => {
  it('reads the server envelope of named events', () => {
    const event = parseConsoleEvent(
      sse('breaker.transition', {
        type: 'breaker.transition',
        tenant_id: 'ten_1',
        namespace_id: 'ns_1',
        site_id: 'sit_a',
        at: '2026-09-17T10:00:00Z',
        data: BREAKER_TRANSITION,
      }),
    );
    expect(event).toEqual({
      type: 'breaker.transition',
      tenantId: 'ten_1',
      namespaceId: 'ns_1',
      siteId: 'sit_a',
      at: '2026-09-17T10:00:00Z',
      data: BREAKER_TRANSITION,
    });
  });

  it('parses every console event type with its payload', () => {
    const payloads: Record<string, Record<string, unknown>> = {
      'identity.state': IDENTITY_STATE,
      'proxy.state': PROXY_STATE,
      alert: ALERT,
      'config.published': CONFIG_PUBLISHED,
      'policy.published': POLICY_PUBLISHED,
    };
    for (const [type, data] of Object.entries(payloads)) {
      const event = parseConsoleEvent(
        sse(type, { type, tenant_id: 'ten_1', namespace_id: 'ns_1', at: '2026-09-17T10:00:00Z', data }),
      );
      expect(event).toEqual({
        type,
        tenantId: 'ten_1',
        namespaceId: 'ns_1',
        at: '2026-09-17T10:00:00Z',
        data,
      });
    }
  });

  it('prefers the named event and falls back to the envelope type', () => {
    expect(parseConsoleEvent(sse('alert', { type: 'proxy.state', data: {} }))?.type).toBe('alert');
    expect(parseConsoleEvent(sse('message', { type: 'alert', data: {} }))?.type).toBe('alert');
  });

  it('treats a missing or malformed payload as empty', () => {
    // Event.Data is omitempty on the server.
    expect(parseConsoleEvent(sse('config.published', { type: 'config.published' }))?.data).toEqual({});
    expect(parseConsoleEvent(sse('alert', { type: 'alert', data: ['not', 'an', 'object'] }))?.data).toEqual(
      {},
    );
    expect(parseConsoleEvent({ type: 'alert', lastEventId: '', data: 'not json' })).toEqual({
      type: 'alert',
      data: {},
    });
  });

  it('ignores untyped messages such as keepalives', () => {
    expect(parseConsoleEvent({ type: 'message', lastEventId: '', data: 'keepalive' })).toBeUndefined();
    expect(parseConsoleEvent({ type: 'message', lastEventId: '', data: { data: {} } })).toBeUndefined();
  });
});

describe('toastForEvent', () => {
  it('describes breaker transitions from from/to with severity by target state', () => {
    const spec = toastForEvent({ type: 'breaker.transition', data: BREAKER_TRANSITION }, t);
    expect(spec?.level).toBe('error');
    expect(spec?.title).toContain('"state":"states.open"');
    expect(spec?.description).toContain('"site":"shop"');
    expect(spec?.description).toContain('"group":"web/search"');
    expect(spec?.description).toContain('"from":"states.closed"');
    expect(spec?.description?.endsWith('(risk_ratio)')).toBe(true);
    const halfOpen = { ...BREAKER_TRANSITION, from: 'open', to: 'half_open', trigger: 'probe', reason: '' };
    expect(toastForEvent({ type: 'breaker.transition', data: halfOpen }, t)).toMatchObject({
      level: 'warning',
    });
    const closed = { ...BREAKER_TRANSITION, from: 'half_open', to: 'closed', reason: '' };
    expect(toastForEvent({ type: 'breaker.transition', data: closed }, t)?.level).toBe('success');
  });

  it('falls back to the site and endpoint group ids', () => {
    const spec = toastForEvent(
      {
        type: 'breaker.transition',
        siteId: 'sit_env',
        data: { ...BREAKER_TRANSITION, site: '', client: '', endpoint_group: '', site_id: '' },
      },
      t,
    );
    expect(spec?.description).toContain('"site":"sit_env"');
    expect(spec?.description).toContain('"group":"eg_1"');
  });

  it('announces site pauses and resumptions instead of breaker states', () => {
    expect(toastForEvent({ type: 'breaker.transition', data: siteSwitch(true, 'maintenance') }, t)).toEqual({
      level: 'warning',
      title: 'events.sitePaused{"site":"shop"}',
      description: 'maintenance',
    });
    expect(toastForEvent({ type: 'breaker.transition', data: siteSwitch(false) }, t)).toEqual({
      level: 'success',
      title: 'events.siteResumed{"site":"shop"}',
      description: undefined,
    });
  });

  it('maps alert severities and shows the alert message', () => {
    expect(toastForEvent({ type: 'alert', data: ALERT }, t)).toEqual({
      level: 'warning',
      title: 'Ban spike on shop',
      description: '12 bans in 5 minutes',
    });
    expect(
      toastForEvent({ type: 'alert', data: { ...ALERT, kind: 'report_backlog', severity: 'critical' } }, t),
    ).toMatchObject({ level: 'error' });
    expect(toastForEvent({ type: 'alert', data: { kind: 'test', severity: 'info' } }, t)).toEqual({
      level: 'info',
      title: 'events.alertTitle',
      description: 'events.alertFallback',
    });
  });

  it('does not toast duplicated or high-volume alerts and other events', () => {
    expect(toastForEvent({ type: 'alert', data: { kind: 'breaker_opened', severity: 'critical' } }, t)).toBe(
      undefined,
    );
    expect(toastForEvent({ type: 'alert', data: { kind: 'identity_expired' } }, t)).toBeUndefined();
    expect(toastForEvent({ type: 'identity.state', data: {} }, t)).toBeUndefined();
    expect(toastForEvent({ type: 'config.published', data: {} }, t)).toBeUndefined();
  });
});

describe('invalidationDomains', () => {
  it('maps events to query domains', () => {
    expect(invalidationDomains({ type: 'breaker.transition', data: {} })).toEqual(
      expect.arrayContaining(['breakers', 'dashboard', 'sites']),
    );
    expect(invalidationDomains({ type: 'identity.state', data: {} })).toEqual(
      expect.arrayContaining(['identities', 'heatmap']),
    );
    expect(invalidationDomains({ type: 'proxy.state', data: {} })).toContain('proxies');
    expect(invalidationDomains({ type: 'config.published', data: {} })).toContain('config');
    expect(invalidationDomains({ type: 'policy.published', data: {} })).toContain('policies');
    expect(invalidationDomains({ type: 'alert', data: {} })).toContain('notifications');
    expect(invalidationDomains({ type: 'unknown.event', data: {} })).toEqual([]);
  });
});

describe('invalidateForEvent', () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it('coalesces bursts into one invalidation per domain', () => {
    const client = new QueryClient();
    const spy = vi.spyOn(client, 'invalidateQueries');
    for (let i = 0; i < 20; i += 1) {
      invalidateForEvent(client, { type: 'identity.state', data: { subject_id: `idn_${i}` } });
    }
    invalidateForEvent(client, { type: 'proxy.state', data: {} });
    expect(spy).not.toHaveBeenCalled();

    vi.advanceTimersByTime(INVALIDATION_COALESCE_MS);
    const keys = spy.mock.calls.map(([filters]) => filters?.queryKey?.[0]);
    expect(keys.filter((k) => k === 'identities')).toHaveLength(1);
    expect(keys).toEqual(expect.arrayContaining(['identities', 'heatmap', 'proxies', 'dashboard']));
    expect(new Set(keys).size).toBe(keys.length);
  });

  it('ignores unknown events and flushes on demand', () => {
    const client = new QueryClient();
    const spy = vi.spyOn(client, 'invalidateQueries');
    invalidateForEvent(client, { type: 'unknown.event', data: {} });
    vi.advanceTimersByTime(INVALIDATION_COALESCE_MS);
    expect(spy).not.toHaveBeenCalled();

    invalidateForEvent(client, { type: 'config.published', data: {} });
    flushInvalidations(client);
    expect(spy).toHaveBeenCalledWith({ queryKey: ['config'] });
    spy.mockClear();
    vi.advanceTimersByTime(INVALIDATION_COALESCE_MS);
    expect(spy).not.toHaveBeenCalled();
  });
});
