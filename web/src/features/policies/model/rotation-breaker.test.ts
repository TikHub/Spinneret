import { describe, expect, it } from 'vitest';

import { policyTemplate } from '../templates';
import { breakerToYaml, emptyBreakerModel, readRevertMode } from './breaker';
import { loadPolicyModel, policyModelToYaml } from './index';
import { emptyRotationModel, rotationToYaml } from './rotation';

describe('rotation model', () => {
  it('generates YAML from the form model', () => {
    const model = {
      ...emptyRotationModel('web-search-rotation'),
      description: 'Search: exclusive leases',
      identityTypes: ['web_cookie'],
      candidateSample: '32',
      leaseTtl: '120s',
      maxConcurrentLeases: '1',
      reuseInterval: '30s',
      quota: [
        { key: 'a', limit: '60', window: '1h' },
        { key: 'b', limit: '500', window: '24h' },
        { key: 'c', limit: '', window: '' },
      ],
      sticky: { enabled: true, ttl: '10m' },
      warmup: { duration: '24h', quotaFactor: '0.3' },
      probe: { weightFactor: '0.1', maxLeases: '2' },
      proxy: { ...emptyRotationModel().proxy, mode: 'bind_identity', kinds: ['residential'] },
    };
    expect(rotationToYaml(model)).toBe(`name: web-search-rotation
description: 'Search: exclusive leases'
identity_types: [web_cookie]
rotation:
  strategy: weighted_random
  candidate_sample: 32
  lease_ttl: 120s
  max_concurrent_leases: 1
  reuse_interval: 30s
  reuse_anchor: released
  reuse_scope: endpoint_group
  quota:
    - { limit: 60, window: 1h }
    - { limit: 500, window: 24h }
  sticky:
    enabled: true
    ttl: 10m
  warmup:
    duration: 24h
    quota_factor: 0.3
  probe:
    weight_factor: 0.1
    max_leases: 2
proxy:
  mode: bind_identity
  kinds: [residential]
  tags: []
  providers: []
  regions: []
  region_match: false
`);
  });

  it('omits unset values so server defaults apply', () => {
    expect(rotationToYaml(emptyRotationModel('minimal'))).toBe(`name: minimal
identity_types: []
rotation:
  strategy: weighted_random
  reuse_anchor: released
  reuse_scope: endpoint_group
  quota: []
  sticky:
    enabled: false
  warmup: {}
  probe: {}
proxy:
  mode: none
  kinds: []
  tags: []
  providers: []
  regions: []
  region_match: false
`);
  });

  it('loads the template and regenerates equivalent YAML', () => {
    const loaded = loadPolicyModel('rotation', policyTemplate('rotation', 'rot'));
    expect(loaded.ok).toBe(true);
    if (!loaded.ok) return;
    expect(loaded.model.leaseTtl).toBe('2m');
    expect(loaded.model.probe.weightFactor).toBe('0.1');
    const again = loadPolicyModel('rotation', policyModelToYaml('rotation', loaded.model));
    expect(again.ok && { ...again.model, quota: [] }).toEqual({ ...loaded.model, quota: [] });
  });

  it('reports fields the form cannot represent', () => {
    const loaded = loadPolicyModel('rotation', 'name: x\nrotation:\n  unknown_field: 1\n');
    expect(loaded).toEqual({ ok: false, error: 'unsupported field rotation.unknown_field' });
    const syntax = loadPolicyModel('rotation', 'name: [x\n');
    expect(syntax.ok).toBe(false);
  });
});

describe('breaker model', () => {
  it('generates YAML from the form model', () => {
    const model = {
      ...emptyBreakerModel('search-breaker'),
      bind: { site: 'shop', client: '', endpointGroup: 'search' },
      window: '60s',
      minRequests: '50',
      trip: { riskRatioGte: '0.4', distinctCaptchaIdentitiesGte: '10', successRatioLte: '0' },
      openDuration: '2m',
      maxOpenDuration: '1h',
      halfOpen: { probeLeasesPer10s: '5', closeMinSamples: '', closeSuccessRatioGte: '0.8' },
      revertRecentCooldowns: 'none' as const,
    };
    expect(breakerToYaml(model)).toBe(`name: search-breaker
bind: { site: shop, endpoint_group: search }
enabled: true
window: 60s
min_requests: 50
trip:
  risk_ratio_gte: 0.4
  distinct_captcha_identities_gte: 10
  success_ratio_lte: 0
open_duration: 2m
max_open_duration: 1h
half_open:
  probe_leases_per_10s: 5
  close_success_ratio_gte: 0.8
revert_recent_cooldowns: none
`);
  });

  it('round-trips the template', () => {
    const template = policyTemplate('breaker', 'brk');
    const loaded = loadPolicyModel('breaker', template);
    expect(loaded.ok).toBe(true);
    if (!loaded.ok) return;
    expect(policyModelToYaml('breaker', loaded.model)).toBe(`name: brk
enabled: true
window: 60s
buckets: 12
min_requests: 50
trip:
  risk_ratio_gte: 0.4
  distinct_captcha_identities_gte: 10
  success_ratio_lte: 0.2
open_duration: 2m
max_open_duration: 1h
reset_open_count_after: 30m
half_open:
  probe_leases_per_10s: 5
  close_min_samples: 5
  close_success_ratio_gte: 0.8
revert_recent_cooldowns: endpoint
`);
  });

  it('maps boolean revert modes', () => {
    expect(readRevertMode(true)).toBe('endpoint');
    expect(readRevertMode(false)).toBe('none');
    expect(readRevertMode(undefined)).toBe('endpoint');
    expect(readRevertMode('all')).toBe('all');
    expect(() => readRevertMode('sometimes')).toThrow();
  });
});
