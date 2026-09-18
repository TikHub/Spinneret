import { describe, expect, it } from 'vitest';

import { coerceScope, isScopeAllowed, scopesForAction } from '../constants';
import { policyTemplate } from '../templates';
import { actionToYaml, actionWhenSummary, emptyActionModel, emptyActionRule, withAction } from './action';
import { loadPolicyModel, policyModelToYaml } from './index';
import {
  emptySignalModel,
  emptySignalRule,
  emptySignalWhen,
  signalToYaml,
  signalWhenSummary,
} from './signal';

describe('signal model', () => {
  it('generates ordered rules with every condition type', () => {
    const model = {
      ...emptySignalModel('web-signals'),
      extends: 'default-signal',
      trustOutcomeHint: true,
      rules: [
        {
          ...emptySignalRule(),
          name: 'proxy-error',
          when: { ...emptySignalWhen(), errorKind: ['proxy_auth', 'conn_refused'] },
          outcome: 'proxy_error',
        },
        {
          ...emptySignalRule(),
          when: {
            ...emptySignalWhen(),
            httpStatusMode: 'list' as const,
            httpStatusList: ['200'],
            businessCode: ['10001', 'E_BAN'],
            markers: ['login_redirect'],
            uriMode: 'regex' as const,
            uriValue: '^/api/v\\d+/user',
            method: ['post'],
          },
          outcome: 'forbidden',
          blame: 'both',
        },
        {
          ...emptySignalRule(),
          name: 'slow',
          when: {
            ...emptySignalWhen(),
            httpStatusMode: 'range' as const,
            httpStatusRange: { lowerOp: 'gte' as const, lower: '200', upperOp: 'lt' as const, upper: '300' },
            uriMode: 'prefix' as const,
            uriValue: '/api/v1/',
            latencyMs: { lowerOp: 'gt' as const, lower: '5000', upperOp: 'lte' as const, upper: '' },
            responseBytes: { lowerOp: 'gte' as const, lower: '', upperOp: 'lt' as const, upper: '128' },
          },
          outcome: 'empty',
        },
        { ...emptySignalRule(), name: 'catch-all', outcome: 'unknown' },
      ],
    };
    expect(signalToYaml(model)).toBe(`name: web-signals
extends: default-signal
trust_outcome_hint: true
rules:
  - name: proxy-error
    when: { error_kind: [proxy_auth, conn_refused] }
    outcome: proxy_error
  - when: { http_status: [200], business_code: ['10001', E_BAN], markers: [login_redirect], uri: { regex: '^/api/v\\d+/user' }, method: [POST] }
    outcome: forbidden
    blame: both
  - name: slow
    when: { http_status: { gte: 200, lt: 300 }, uri: { prefix: /api/v1/ }, latency_ms: { gt: 5000 }, response_bytes: { lt: 128 } }
    outcome: empty
  - name: catch-all
    when: {}
    outcome: unknown
`);
  });

  it('loads the template rules in order and summarizes conditions', () => {
    const loaded = loadPolicyModel('signal', policyTemplate('signal', 'sig'));
    expect(loaded.ok).toBe(true);
    if (!loaded.ok) return;
    expect(loaded.model.rules.map((r) => r.name)).toEqual([
      'proxy-error',
      'network-error',
      'captcha',
      'login-redirect',
      'rate-limited',
      'target-error',
      'empty-list',
      'client-error',
      'auth-invalid',
      'forbidden',
      'success',
    ]);
    const target = loaded.model.rules[5];
    expect(target?.when.httpStatusMode).toBe('range');
    expect(target && signalWhenSummary(target.when)).toBe('{ http_status: { gte: 500 } }');
    const regenerated = policyModelToYaml('signal', loaded.model);
    expect(regenerated).toContain('  - name: success\n    when: { http_status: { gte: 200, lt: 300 } }\n');
    const again = loadPolicyModel('signal', regenerated);
    expect(again.ok && again.model.rules.map((r) => r.when)).toEqual(loaded.model.rules.map((r) => r.when));
  });

  it('keeps YAML the form would change in the YAML editor', () => {
    const both = loadPolicyModel(
      'signal',
      'name: s\nrules:\n  - when: { uri: { prefix: /a, regex: "^/b" } }\n    outcome: success\n',
    );
    expect(both.ok).toBe(false);
    const blank = loadPolicyModel(
      'signal',
      'name: s\nrules:\n  - when: { markers: [""] }\n    outcome: success\n',
    );
    expect(blank.ok).toBe(false);
    const described = loadPolicyModel(
      'signal',
      'name: s\ndescription: |\n  Line one\n  Line two\nrules: []\n',
    );
    expect(described.ok && policyModelToYaml('signal', described.model)).toContain(
      'description: "Line one\\nLine two\\n"',
    );
  });

  it('rejects ambiguous ranges', () => {
    const loaded = loadPolicyModel(
      'signal',
      'name: s\nrules:\n  - when: { latency_ms: { gt: 1, gte: 2 } }\n    outcome: success\n',
    );
    expect(loaded.ok).toBe(false);
  });
});

describe('action model', () => {
  it('generates rules, escalation, health and cross attribution', () => {
    const model = {
      ...emptyActionModel('web-search-actions'),
      mode: 'shadow',
      rules: [
        { ...emptyActionRule(), name: 'rate-limited', base: '60s', multiplier: '2', max: '30m' },
        {
          ...withAction(emptyActionRule(), 'ban'),
          name: 'captcha-ban',
          outcomes: ['captcha', 'forbidden'],
          countEnabled: true,
          countGte: '3',
          countWithin: '24h',
          duration: '12h',
        },
        { ...withAction(emptyActionRule(), 'expire'), outcomes: ['auth_invalid'] },
      ],
      escalation: [{ key: 'e1', gte: '3', within: '30d', duration: 'permanent' }],
      health: {
        ...emptyActionModel().health,
        baseline: '70',
        observations: [{ key: 'o1', outcome: 'empty', value: '40' }],
        quarantineDuration: '12h',
      },
      banExpiryState: 'active',
      crossAttribution: {
        enabled: false,
        window: '10m',
        proxyDistinctIdentities: '',
        identityDistinctProxies: '5',
      },
    };
    expect(actionToYaml(model)).toBe(`name: web-search-actions
mode: shadow
rules:
  - name: rate-limited
    when: { outcome: rate_limited }
    action: cooldown
    scope: identity_endpoint
    base: 60s
    multiplier: 2
    max: 30m
  - name: captcha-ban
    when: { outcome: [captcha, forbidden], count: { gte: 3, within: 24h } }
    action: ban
    scope: identity
    duration: 12h
  - when: { outcome: auth_invalid }
    action: expire
    scope: identity
escalation:
  - when: { bans: { gte: 3, within: 30d } }
    duration: permanent
health:
  baseline: 70
  observations: { empty: 40 }
  quarantine_duration: 12h
ban_expiry_state: active
cross_attribution:
  enabled: false
  window: 10m
  identity_distinct_proxies: 5
`);
  });

  it('round-trips the template', () => {
    const loaded = loadPolicyModel('action', policyTemplate('action', 'act'));
    expect(loaded.ok).toBe(true);
    if (!loaded.ok) return;
    expect(loaded.model.rules).toHaveLength(7);
    expect(loaded.model.escalation.map((s) => s.duration)).toEqual(['72h', 'permanent']);
    expect(actionWhenSummary(loaded.model.rules[1]!)).toBe(
      '{ outcome: empty, count: { gte: 3, within: 10m } }',
    );
    const again = loadPolicyModel('action', policyModelToYaml('action', loaded.model));
    const strip = (m: typeof loaded.model) => ({
      ...m,
      rules: m.rules.map((r) => ({ ...r, key: '' })),
      escalation: m.escalation.map((s) => ({ ...s, key: '' })),
    });
    expect(again.ok && strip(again.model)).toEqual(strip(loaded.model));
  });
});

describe('scope and action compatibility', () => {
  it('lists the scopes each action supports', () => {
    expect(scopesForAction('cooldown')).toEqual([
      'identity_endpoint',
      'identity_site',
      'account',
      'proxy_site',
      'proxy',
    ]);
    expect(scopesForAction('ban')).toEqual(['identity', 'account', 'proxy']);
    expect(scopesForAction('expire')).toEqual(['identity']);
    expect(scopesForAction('quarantine')).toEqual(['identity', 'proxy']);
    expect(scopesForAction('activate')).toEqual([]);
    expect(isScopeAllowed('ban', 'proxy_site')).toBe(false);
    expect(isScopeAllowed('quarantine', 'proxy')).toBe(true);
  });

  it('coerces scopes and clears unused parameters when the action changes', () => {
    expect(coerceScope('ban', 'identity_endpoint')).toBe('identity');
    expect(coerceScope('ban', 'account')).toBe('account');
    expect(coerceScope('cooldown', 'identity')).toBe('identity_site');
    expect(coerceScope('expire', 'proxy')).toBe('identity');

    const cooldown = { ...emptyActionRule(), scope: 'proxy_site', base: '2m', max: '30m' };
    const quarantine = withAction(cooldown, 'quarantine');
    expect(quarantine).toMatchObject({
      action: 'quarantine',
      scope: 'identity',
      base: '',
      max: '',
      duration: '',
    });
    const ban = withAction({ ...quarantine, scope: 'proxy', duration: '24h' }, 'ban');
    expect(ban).toMatchObject({ scope: 'proxy', duration: '24h' });
    expect(withAction({ ...ban, duration: 'permanent' }, 'quarantine').duration).toBe('');
    expect(withAction(ban, 'expire').duration).toBe('');
  });

  it('does not load parameters the rule action does not support', () => {
    const banWithBase = loadPolicyModel(
      'action',
      'name: a\nrules:\n  - when: { outcome: banned }\n    action: ban\n    scope: identity\n    duration: 1h\n    base: 1m\n',
    );
    expect(banWithBase).toMatchObject({ ok: false, error: 'rules[0].base is only supported for cooldown' });
    const expireWithDuration = loadPolicyModel(
      'action',
      'name: a\nrules:\n  - when: { outcome: auth_invalid }\n    action: expire\n    scope: identity\n    duration: 1h\n',
    );
    expect(expireWithDuration.ok).toBe(false);
  });

  it('normalizes cooldown on identity when loading', () => {
    const loaded = loadPolicyModel(
      'action',
      'name: a\nrules:\n  - when: { outcome: captcha }\n    action: cooldown\n    scope: identity\n    base: 1m\n',
    );
    expect(loaded.ok && loaded.model.rules[0]?.scope).toBe('identity_site');
  });
});
