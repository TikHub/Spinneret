import { coerceScope } from '../constants';
import { blockMap, blockSeq, emitYaml, flowMap, flowText, scalar, type YamlNode } from '../yaml/emit';
import {
  asObject,
  bool,
  checkKeys,
  META_KEYS,
  metaEntries,
  ModelReadError,
  newKey,
  num,
  readBool,
  readMeta,
  readObjectList,
  readStringList,
  readText,
  section,
  text,
  type PolicyMetaModel,
  type YamlObject,
} from './common';

export interface ActionRuleModel {
  key: string;
  name: string;
  outcomes: string[];
  countEnabled: boolean;
  countGte: string;
  countWithin: string;
  action: string;
  scope: string;
  /** Cooldown parameters. */
  base: string;
  multiplier: string;
  max: string;
  maxExponent: string;
  failureResetAfter: string;
  /** Ban (may be "permanent") and quarantine duration. */
  duration: string;
}

export interface EscalationStepModel {
  key: string;
  gte: string;
  within: string;
  duration: string;
}

export interface ObservationModel {
  key: string;
  outcome: string;
  value: string;
}

export interface HealthModel {
  alpha: string;
  baseline: string;
  tau: string;
  observations: ObservationModel[];
  endpointLowScore: string;
  endpointLowMinSamples: string;
  endpointLowCooldown: string;
  quarantineScore: string;
  quarantineMinSamples: string;
  quarantineDuration: string;
}

/** Form model of an action policy (spec section 7). */
export interface ActionModel extends PolicyMetaModel {
  extends: string;
  mode: string;
  rules: ActionRuleModel[];
  escalation: EscalationStepModel[];
  health: HealthModel;
  banExpiryState: string;
  crossAttribution: {
    enabled: boolean;
    window: string;
    proxyDistinctIdentities: string;
    identityDistinctProxies: string;
  };
}

/** Server defaults, shown as placeholders for empty fields. */
export const ACTION_DEFAULTS = {
  multiplier: '1',
  max: '24h',
  maxExponent: '10',
  failureResetAfter: '1h',
  alpha: '0.1',
  baseline: '70',
  tau: '6h',
  endpointLowScore: '15',
  endpointLowMinSamples: '10',
  endpointLowCooldown: '6h',
  quarantineScore: '20',
  quarantineMinSamples: '10',
  quarantineDuration: '24h',
  crossWindow: '10m',
  proxyDistinctIdentities: '3',
  identityDistinctProxies: '3',
} as const;

export function emptyActionRule(): ActionRuleModel {
  return {
    key: newKey(),
    name: '',
    outcomes: ['rate_limited'],
    countEnabled: false,
    countGte: '',
    countWithin: '',
    action: 'cooldown',
    scope: 'identity_endpoint',
    base: '60s',
    multiplier: '',
    max: '',
    maxExponent: '',
    failureResetAfter: '',
    duration: '',
  };
}

export function emptyEscalationStep(): EscalationStepModel {
  return { key: newKey(), gte: '', within: '', duration: '' };
}

export function emptyHealth(): HealthModel {
  return {
    alpha: '',
    baseline: '',
    tau: '',
    observations: [],
    endpointLowScore: '',
    endpointLowMinSamples: '',
    endpointLowCooldown: '',
    quarantineScore: '',
    quarantineMinSamples: '',
    quarantineDuration: '',
  };
}

export function emptyActionModel(name = ''): ActionModel {
  return {
    name,
    description: '',
    bind: null,
    extends: '',
    mode: 'enforce',
    rules: [],
    escalation: [],
    health: emptyHealth(),
    banExpiryState: 'pending',
    crossAttribution: { enabled: true, window: '', proxyDistinctIdentities: '', identityDistinctProxies: '' },
  };
}

/**
 * Changes the action of a rule, keeping the scope when compatible (otherwise
 * the first compatible scope) and clearing parameters the action does not use.
 */
export function withAction(rule: ActionRuleModel, action: string): ActionRuleModel {
  const cooldown = action === 'cooldown';
  const timed = action === 'ban' || action === 'quarantine';
  return {
    ...rule,
    action,
    scope: coerceScope(action, rule.scope),
    base: cooldown ? rule.base : '',
    multiplier: cooldown ? rule.multiplier : '',
    max: cooldown ? rule.max : '',
    maxExponent: cooldown ? rule.maxExponent : '',
    failureResetAfter: cooldown ? rule.failureResetAfter : '',
    duration: timed ? (action === 'quarantine' && rule.duration === 'permanent' ? '' : rule.duration) : '',
  };
}

const COOLDOWN_KEYS = ['base', 'multiplier', 'max', 'max_exponent', 'failure_reset_after'] as const;

const RULE_KEYS = [
  'name',
  'when',
  'action',
  'scope',
  'base',
  'multiplier',
  'max',
  'max_exponent',
  'duration',
  'failure_reset_after',
];

function readRule(rule: YamlObject, index: number): ActionRuleModel {
  const path = `rules[${index}]`;
  checkKeys(rule, RULE_KEYS, path);
  const when = asObject(rule.when, `${path}.when`);
  checkKeys(when, ['outcome', 'count'], `${path}.when`);
  const count = when.count == null ? undefined : asObject(when.count, `${path}.when.count`);
  if (count) checkKeys(count, ['gte', 'within'], `${path}.when.count`);
  const action = readText(rule, 'action', path);
  const scope = readText(rule, 'scope', path);
  // The form only keeps the parameters of the selected action; keep such YAML in the YAML
  // editor so the server reports the misplaced field instead of the form dropping it.
  if (action !== 'cooldown') {
    const misplaced = COOLDOWN_KEYS.find((key) => rule[key] != null);
    if (misplaced) throw new ModelReadError(`${path}.${misplaced} is only supported for cooldown`);
  }
  if (action !== 'ban' && action !== 'quarantine' && rule.duration != null) {
    throw new ModelReadError(`${path}.duration is not supported for ${action || 'this action'}`);
  }
  return {
    key: newKey(),
    name: readText(rule, 'name', path),
    outcomes: readStringList(when, 'outcome', `${path}.when`),
    countEnabled: count !== undefined,
    countGte: count ? readText(count, 'gte', `${path}.when.count`) : '',
    countWithin: count ? readText(count, 'within', `${path}.when.count`) : '',
    action,
    scope: action === 'cooldown' && scope === 'identity' ? 'identity_site' : scope,
    base: readText(rule, 'base', path),
    multiplier: readText(rule, 'multiplier', path),
    max: readText(rule, 'max', path),
    maxExponent: readText(rule, 'max_exponent', path),
    failureResetAfter: readText(rule, 'failure_reset_after', path),
    duration: readText(rule, 'duration', path),
  };
}

function readEscalation(step: YamlObject, index: number): EscalationStepModel {
  const path = `escalation[${index}]`;
  checkKeys(step, ['when', 'duration'], path);
  const when = asObject(step.when, `${path}.when`);
  checkKeys(when, ['bans'], `${path}.when`);
  const bans = asObject(when.bans, `${path}.when.bans`);
  checkKeys(bans, ['gte', 'within'], `${path}.when.bans`);
  return {
    key: newKey(),
    gte: readText(bans, 'gte', `${path}.when.bans`),
    within: readText(bans, 'within', `${path}.when.bans`),
    duration: readText(step, 'duration', path),
  };
}

const HEALTH_KEYS = [
  'alpha',
  'baseline',
  'tau',
  'observations',
  'endpoint_low_score',
  'endpoint_low_min_samples',
  'endpoint_low_cooldown',
  'quarantine_score',
  'quarantine_min_samples',
  'quarantine_duration',
];

function readHealth(value: YamlObject): HealthModel {
  const h = asObject(value.health, 'health');
  checkKeys(h, HEALTH_KEYS, 'health');
  const obs = asObject(h.observations, 'health.observations');
  const observations = Object.entries(obs).map(([outcome, v]) => {
    if (v !== null && typeof v === 'object') {
      throw new ModelReadError(`health.observations.${outcome} must be a number`);
    }
    return { key: newKey(), outcome, value: v === null ? '' : String(v) };
  });
  return {
    alpha: readText(h, 'alpha', 'health'),
    baseline: readText(h, 'baseline', 'health'),
    tau: readText(h, 'tau', 'health'),
    observations,
    endpointLowScore: readText(h, 'endpoint_low_score', 'health'),
    endpointLowMinSamples: readText(h, 'endpoint_low_min_samples', 'health'),
    endpointLowCooldown: readText(h, 'endpoint_low_cooldown', 'health'),
    quarantineScore: readText(h, 'quarantine_score', 'health'),
    quarantineMinSamples: readText(h, 'quarantine_min_samples', 'health'),
    quarantineDuration: readText(h, 'quarantine_duration', 'health'),
  };
}

export function readActionModel(value: YamlObject): ActionModel {
  checkKeys(
    value,
    [
      ...META_KEYS,
      'extends',
      'mode',
      'rules',
      'escalation',
      'health',
      'ban_expiry_state',
      'cross_attribution',
    ],
    '',
  );
  const cross = asObject(value.cross_attribution, 'cross_attribution');
  checkKeys(
    cross,
    ['enabled', 'window', 'proxy_distinct_identities', 'identity_distinct_proxies'],
    'cross_attribution',
  );
  return {
    ...readMeta(value),
    extends: readText(value, 'extends', ''),
    mode: readText(value, 'mode', '') || 'enforce',
    rules: readObjectList(value, 'rules', '').map(readRule),
    escalation: readObjectList(value, 'escalation', '').map(readEscalation),
    health: readHealth(value),
    banExpiryState: readText(value, 'ban_expiry_state', '') || 'pending',
    crossAttribution: {
      enabled: readBool(cross, 'enabled', 'cross_attribution', true),
      window: readText(cross, 'window', 'cross_attribution'),
      proxyDistinctIdentities: readText(cross, 'proxy_distinct_identities', 'cross_attribution'),
      identityDistinctProxies: readText(cross, 'identity_distinct_proxies', 'cross_attribution'),
    },
  };
}

/** The `when` block of an action rule; a single outcome is written as a scalar. */
export function actionWhenNode(rule: ActionRuleModel): YamlNode {
  const outcomes = rule.outcomes.map((o) => o.trim()).filter(Boolean);
  const outcome =
    outcomes.length === 1
      ? scalar(outcomes[0] ?? '')
      : { type: 'seq' as const, items: outcomes.map(scalar), flow: true };
  return flowMap([
    ['outcome', outcome],
    [
      'count',
      rule.countEnabled
        ? flowMap([
            ['gte', num(rule.countGte)],
            ['within', text(rule.countWithin)],
          ])
        : undefined,
    ],
  ]);
}

export function actionWhenSummary(rule: ActionRuleModel): string {
  return flowText(actionWhenNode(rule));
}

function ruleNode(rule: ActionRuleModel): YamlNode {
  const cooldown = rule.action === 'cooldown';
  return blockMap([
    ['name', text(rule.name)],
    ['when', actionWhenNode(rule)],
    ['action', text(rule.action)],
    ['scope', text(rule.scope)],
    ['base', cooldown ? text(rule.base) : undefined],
    ['multiplier', cooldown ? num(rule.multiplier) : undefined],
    ['max', cooldown ? text(rule.max) : undefined],
    ['max_exponent', cooldown ? num(rule.maxExponent) : undefined],
    ['failure_reset_after', cooldown ? text(rule.failureResetAfter) : undefined],
    ['duration', rule.action === 'ban' || rule.action === 'quarantine' ? text(rule.duration) : undefined],
  ]);
}

function healthNode(h: HealthModel): YamlNode | undefined {
  const observations = h.observations.filter((o) => o.outcome.trim() !== '');
  return section([
    ['alpha', num(h.alpha)],
    ['baseline', num(h.baseline)],
    ['tau', text(h.tau)],
    [
      'observations',
      observations.length === 0
        ? undefined
        : flowMap(observations.map((o) => [o.outcome.trim(), num(o.value)] as const)),
    ],
    ['endpoint_low_score', num(h.endpointLowScore)],
    ['endpoint_low_min_samples', num(h.endpointLowMinSamples)],
    ['endpoint_low_cooldown', text(h.endpointLowCooldown)],
    ['quarantine_score', num(h.quarantineScore)],
    ['quarantine_min_samples', num(h.quarantineMinSamples)],
    ['quarantine_duration', text(h.quarantineDuration)],
  ]);
}

/** Generates action policy YAML from the form model. */
export function actionToYaml(m: ActionModel): string {
  const c = m.crossAttribution;
  return emitYaml(
    blockMap([
      ...metaEntries(m),
      ['extends', text(m.extends)],
      ['mode', text(m.mode)],
      ['rules', blockSeq(m.rules.map(ruleNode))],
      [
        'escalation',
        blockSeq(
          m.escalation.map((step) =>
            blockMap([
              [
                'when',
                flowMap([
                  [
                    'bans',
                    flowMap([
                      ['gte', num(step.gte)],
                      ['within', text(step.within)],
                    ]),
                  ],
                ]),
              ],
              ['duration', text(step.duration)],
            ]),
          ),
        ),
      ],
      ['health', healthNode(m.health)],
      ['ban_expiry_state', text(m.banExpiryState)],
      [
        'cross_attribution',
        blockMap([
          ['enabled', bool(c.enabled)],
          ['window', text(c.window)],
          ['proxy_distinct_identities', num(c.proxyDistinctIdentities)],
          ['identity_distinct_proxies', num(c.identityDistinctProxies)],
        ]),
      ],
    ]),
  );
}
