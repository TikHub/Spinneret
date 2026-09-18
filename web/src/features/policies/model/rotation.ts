import { blockMap, blockSeq, emitYaml, flowMap, type YamlNode } from '../yaml/emit';
import {
  asObject,
  bool,
  checkKeys,
  list,
  META_KEYS,
  metaEntries,
  newKey,
  num,
  readBool,
  readMeta,
  readObjectList,
  readStringList,
  readText,
  text,
  type PolicyMetaModel,
  type YamlObject,
} from './common';

export interface QuotaModel {
  key: string;
  limit: string;
  window: string;
}

export interface RotationProxyModel {
  mode: string;
  kinds: string[];
  tags: string[];
  providers: string[];
  regions: string[];
  regionMatch: boolean;
  rebindTolerance: string;
  maxRebindsPerDay: string;
}

/** Form model of a rotation policy (spec section 7). */
export interface RotationModel extends PolicyMetaModel {
  identityTypes: string[];
  strategy: string;
  candidateSample: string;
  leaseTtl: string;
  maxLeaseLifetime: string;
  maxConcurrentLeases: string;
  reuseInterval: string;
  reuseAnchor: string;
  reuseScope: string;
  quota: QuotaModel[];
  sticky: { enabled: boolean; ttl: string };
  warmup: { duration: string; quotaFactor: string };
  probe: { weightFactor: string; maxLeases: string };
  proxy: RotationProxyModel;
}

/** Server defaults, shown as placeholders for empty fields. */
export const ROTATION_DEFAULTS = {
  strategy: 'weighted_random',
  candidateSample: '32',
  leaseTtl: '2m',
  maxLeaseLifetime: '30m',
  maxConcurrentLeases: '1',
  reuseInterval: '0s',
  reuseAnchor: 'released',
  reuseScope: 'endpoint_group',
  stickyTtl: '10m',
  warmupDuration: '0s',
  warmupQuotaFactor: '1',
  probeWeightFactor: '0.1',
  probeMaxLeases: '2',
  proxyMode: 'none',
  rebindTolerance: '5m',
  maxRebindsPerDay: '3',
} as const;

export function emptyRotationModel(name = ''): RotationModel {
  return {
    name,
    description: '',
    bind: null,
    identityTypes: [],
    strategy: ROTATION_DEFAULTS.strategy,
    candidateSample: '',
    leaseTtl: '',
    maxLeaseLifetime: '',
    maxConcurrentLeases: '',
    reuseInterval: '',
    reuseAnchor: ROTATION_DEFAULTS.reuseAnchor,
    reuseScope: ROTATION_DEFAULTS.reuseScope,
    quota: [],
    sticky: { enabled: false, ttl: '' },
    warmup: { duration: '', quotaFactor: '' },
    probe: { weightFactor: '', maxLeases: '' },
    proxy: {
      mode: ROTATION_DEFAULTS.proxyMode,
      kinds: [],
      tags: [],
      providers: [],
      regions: [],
      regionMatch: false,
      rebindTolerance: '',
      maxRebindsPerDay: '',
    },
  };
}

const ROTATION_KEYS = [
  'strategy',
  'candidate_sample',
  'lease_ttl',
  'max_lease_lifetime',
  'max_concurrent_leases',
  'reuse_interval',
  'reuse_anchor',
  'reuse_scope',
  'quota',
  'sticky',
  'warmup',
  'probe',
];
const PROXY_KEYS = [
  'mode',
  'kinds',
  'tags',
  'providers',
  'regions',
  'region_match',
  'rebind_tolerance',
  'max_rebinds_per_day',
];

function readProxy(doc: YamlObject): RotationProxyModel {
  const p = asObject(doc.proxy, 'proxy');
  checkKeys(p, PROXY_KEYS, 'proxy');
  return {
    mode: readText(p, 'mode', 'proxy') || ROTATION_DEFAULTS.proxyMode,
    kinds: readStringList(p, 'kinds', 'proxy'),
    tags: readStringList(p, 'tags', 'proxy'),
    providers: readStringList(p, 'providers', 'proxy'),
    regions: readStringList(p, 'regions', 'proxy'),
    regionMatch: readBool(p, 'region_match', 'proxy', false),
    rebindTolerance: readText(p, 'rebind_tolerance', 'proxy'),
    maxRebindsPerDay: readText(p, 'max_rebinds_per_day', 'proxy'),
  };
}

/** Builds the form model from parsed YAML; throws ModelReadError for unsupported content. */
export function readRotationModel(value: YamlObject): RotationModel {
  checkKeys(value, [...META_KEYS, 'identity_types', 'rotation', 'proxy'], '');
  const r = asObject(value.rotation, 'rotation');
  checkKeys(r, ROTATION_KEYS, 'rotation');
  const sticky = asObject(r.sticky, 'rotation.sticky');
  checkKeys(sticky, ['enabled', 'ttl'], 'rotation.sticky');
  const warmup = asObject(r.warmup, 'rotation.warmup');
  checkKeys(warmup, ['duration', 'quota_factor'], 'rotation.warmup');
  const probe = asObject(r.probe, 'rotation.probe');
  checkKeys(probe, ['weight_factor', 'max_leases'], 'rotation.probe');
  const quota = readObjectList(r, 'quota', 'rotation').map((q, i) => {
    checkKeys(q, ['limit', 'window'], `rotation.quota[${i}]`);
    return { key: newKey(), limit: readText(q, 'limit', ''), window: readText(q, 'window', '') };
  });
  return {
    ...readMeta(value),
    identityTypes: readStringList(value, 'identity_types', ''),
    strategy: readText(r, 'strategy', 'rotation') || ROTATION_DEFAULTS.strategy,
    candidateSample: readText(r, 'candidate_sample', 'rotation'),
    leaseTtl: readText(r, 'lease_ttl', 'rotation'),
    maxLeaseLifetime: readText(r, 'max_lease_lifetime', 'rotation'),
    maxConcurrentLeases: readText(r, 'max_concurrent_leases', 'rotation'),
    reuseInterval: readText(r, 'reuse_interval', 'rotation'),
    reuseAnchor: readText(r, 'reuse_anchor', 'rotation') || ROTATION_DEFAULTS.reuseAnchor,
    reuseScope: readText(r, 'reuse_scope', 'rotation') || ROTATION_DEFAULTS.reuseScope,
    quota,
    sticky: {
      enabled: readBool(sticky, 'enabled', 'rotation.sticky', false),
      ttl: readText(sticky, 'ttl', 'rotation.sticky'),
    },
    warmup: {
      duration: readText(warmup, 'duration', 'rotation.warmup'),
      quotaFactor: readText(warmup, 'quota_factor', 'rotation.warmup'),
    },
    probe: {
      weightFactor: readText(probe, 'weight_factor', 'rotation.probe'),
      maxLeases: readText(probe, 'max_leases', 'rotation.probe'),
    },
    proxy: readProxy(value),
  };
}

function rotationNode(m: RotationModel): YamlNode {
  const quota = m.quota
    .filter((q) => q.limit.trim() !== '' || q.window.trim() !== '')
    .map((q) =>
      flowMap([
        ['limit', num(q.limit)],
        ['window', text(q.window)],
      ]),
    );
  return blockMap([
    ['strategy', text(m.strategy)],
    ['candidate_sample', num(m.candidateSample)],
    ['lease_ttl', text(m.leaseTtl)],
    ['max_lease_lifetime', text(m.maxLeaseLifetime)],
    ['max_concurrent_leases', num(m.maxConcurrentLeases)],
    ['reuse_interval', text(m.reuseInterval)],
    ['reuse_anchor', text(m.reuseAnchor)],
    ['reuse_scope', text(m.reuseScope)],
    ['quota', blockSeq(quota)],
    [
      'sticky',
      blockMap([
        ['enabled', bool(m.sticky.enabled)],
        ['ttl', text(m.sticky.ttl)],
      ]),
    ],
    [
      'warmup',
      blockMap([
        ['duration', text(m.warmup.duration)],
        ['quota_factor', num(m.warmup.quotaFactor)],
      ]),
    ],
    [
      'probe',
      blockMap([
        ['weight_factor', num(m.probe.weightFactor)],
        ['max_leases', num(m.probe.maxLeases)],
      ]),
    ],
  ]);
}

/** Generates rotation policy YAML from the form model. */
export function rotationToYaml(m: RotationModel): string {
  const p = m.proxy;
  return emitYaml(
    blockMap([
      ...metaEntries(m),
      ['identity_types', list(m.identityTypes)],
      ['rotation', rotationNode(m)],
      [
        'proxy',
        blockMap([
          ['mode', text(p.mode)],
          ['kinds', list(p.kinds)],
          ['tags', list(p.tags)],
          ['providers', list(p.providers)],
          ['regions', list(p.regions)],
          ['region_match', bool(p.regionMatch)],
          ['rebind_tolerance', text(p.rebindTolerance)],
          ['max_rebinds_per_day', num(p.maxRebindsPerDay)],
        ]),
      ],
    ]),
  );
}
