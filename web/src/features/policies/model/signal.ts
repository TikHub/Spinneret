import { blockMap, blockSeq, emitYaml, flowMap, flowText, scalarSeq, type YamlNode } from '../yaml/emit';
import { type YamlValue } from '../yaml/parse';
import {
  asObject,
  bool,
  checkKeys,
  isObject,
  META_KEYS,
  metaEntries,
  ModelReadError,
  newKey,
  num,
  optionalList,
  readBool,
  readMeta,
  readObjectList,
  readStringList,
  readText,
  text,
  type PolicyMetaModel,
  type YamlObject,
} from './common';

/** Integer range with exclusive/inclusive bounds; empty bounds are open. */
export interface RangeModel {
  lowerOp: 'gte' | 'gt';
  lower: string;
  upperOp: 'lte' | 'lt';
  upper: string;
}

export type HttpStatusMode = 'any' | 'list' | 'range';
export type UriMode = 'any' | 'prefix' | 'regex';

export interface SignalWhenModel {
  httpStatusMode: HttpStatusMode;
  httpStatusList: string[];
  httpStatusRange: RangeModel;
  businessCode: string[];
  errorKind: string[];
  markers: string[];
  uriMode: UriMode;
  uriValue: string;
  method: string[];
  latencyMs: RangeModel;
  responseBytes: RangeModel;
}

export interface SignalRuleModel {
  key: string;
  name: string;
  when: SignalWhenModel;
  outcome: string;
  /** "" keeps the default blame of the outcome. */
  blame: string;
}

/** Form model of a signal policy (ordered classification rules). */
export interface SignalModel extends PolicyMetaModel {
  extends: string;
  trustOutcomeHint: boolean;
  rules: SignalRuleModel[];
}

export function emptyRange(): RangeModel {
  return { lowerOp: 'gte', lower: '', upperOp: 'lte', upper: '' };
}

export function emptySignalWhen(): SignalWhenModel {
  return {
    httpStatusMode: 'any',
    httpStatusList: [],
    httpStatusRange: emptyRange(),
    businessCode: [],
    errorKind: [],
    markers: [],
    uriMode: 'any',
    uriValue: '',
    method: [],
    latencyMs: emptyRange(),
    responseBytes: emptyRange(),
  };
}

export function emptySignalRule(): SignalRuleModel {
  return { key: newKey(), name: '', when: emptySignalWhen(), outcome: 'success', blame: '' };
}

export function emptySignalModel(name = ''): SignalModel {
  return { name, description: '', bind: null, extends: '', trustOutcomeHint: false, rules: [] };
}

const RANGE_KEYS = ['gte', 'gt', 'lte', 'lt'];

function readRange(value: YamlValue | undefined, path: string): RangeModel {
  const r = asObject(value, path);
  checkKeys(r, RANGE_KEYS, path);
  if (RANGE_KEYS.every((key) => r[key] == null)) {
    throw new ModelReadError(`${path} must set at least one of gte, gt, lte, lt`);
  }
  if (r.gte != null && r.gt != null) throw new ModelReadError(`${path}: gte and gt are mutually exclusive`);
  if (r.lte != null && r.lt != null) throw new ModelReadError(`${path}: lte and lt are mutually exclusive`);
  return {
    lowerOp: r.gt != null ? 'gt' : 'gte',
    lower: readText(r, r.gt != null ? 'gt' : 'gte', path),
    upperOp: r.lt != null ? 'lt' : 'lte',
    upper: readText(r, r.lt != null ? 'lt' : 'lte', path),
  };
}

const WHEN_KEYS = [
  'http_status',
  'business_code',
  'error_kind',
  'markers',
  'uri',
  'method',
  'latency_ms',
  'response_bytes',
];

const LIST_WHEN_KEYS = ['http_status', 'business_code', 'error_kind', 'markers', 'method'];

function readWhen(value: YamlValue | undefined, path: string): SignalWhenModel {
  const w = asObject(value, path);
  checkKeys(w, WHEN_KEYS, path);
  // The form omits empty conditions, which the server rejects when written explicitly.
  for (const key of LIST_WHEN_KEYS) {
    const list = w[key];
    if (Array.isArray(list) && list.length === 0)
      throw new ModelReadError(`${path}.${key} must not be empty`);
  }
  const when = emptySignalWhen();
  const status = w.http_status;
  if (isObject(status)) {
    when.httpStatusMode = 'range';
    when.httpStatusRange = readRange(status, `${path}.http_status`);
  } else if (status !== undefined && status !== null) {
    when.httpStatusMode = 'list';
    when.httpStatusList = readStringList(w, 'http_status', path);
  }
  when.businessCode = readStringList(w, 'business_code', path);
  when.errorKind = readStringList(w, 'error_kind', path);
  when.markers = readStringList(w, 'markers', path);
  if (w.uri !== undefined && w.uri !== null) {
    const uri = asObject(w.uri, `${path}.uri`);
    checkKeys(uri, ['prefix', 'regex'], `${path}.uri`);
    if (uri.regex != null && uri.prefix != null) {
      throw new ModelReadError(`${path}.uri: prefix and regex are mutually exclusive`);
    }
    if (uri.regex == null && uri.prefix == null)
      throw new ModelReadError(`${path}.uri must set prefix or regex`);
    if (uri.regex != null) {
      when.uriMode = 'regex';
      when.uriValue = readText(uri, 'regex', `${path}.uri`);
    } else {
      when.uriMode = 'prefix';
      when.uriValue = readText(uri, 'prefix', `${path}.uri`);
    }
  }
  when.method = readStringList(w, 'method', path);
  if (w.latency_ms != null) when.latencyMs = readRange(w.latency_ms, `${path}.latency_ms`);
  if (w.response_bytes != null) when.responseBytes = readRange(w.response_bytes, `${path}.response_bytes`);
  return when;
}

export function readSignalModel(value: YamlObject): SignalModel {
  checkKeys(value, [...META_KEYS, 'extends', 'trust_outcome_hint', 'rules'], '');
  const rules = readObjectList(value, 'rules', '').map((rule, i) => {
    const path = `rules[${i}]`;
    checkKeys(rule, ['name', 'when', 'outcome', 'blame'], path);
    return {
      key: newKey(),
      name: readText(rule, 'name', path),
      when: readWhen(rule.when, `${path}.when`),
      outcome: readText(rule, 'outcome', path),
      blame: readText(rule, 'blame', path),
    };
  });
  return {
    ...readMeta(value),
    extends: readText(value, 'extends', ''),
    trustOutcomeHint: readBool(value, 'trust_outcome_hint', '', false),
    rules,
  };
}

/** Range as a flow mapping; undefined when both bounds are empty. */
export function rangeNode(range: RangeModel): YamlNode | undefined {
  const lower = num(range.lower);
  const upper = num(range.upper);
  if (!lower && !upper) return undefined;
  return flowMap([
    [range.lowerOp, lower],
    [range.upperOp, upper],
  ]);
}

function statusNode(when: SignalWhenModel): YamlNode | undefined {
  if (when.httpStatusMode === 'range') return rangeNode(when.httpStatusRange);
  if (when.httpStatusMode !== 'list') return undefined;
  const values = when.httpStatusList.map(num).filter((n): n is YamlNode => n !== undefined);
  return values.length === 0 ? undefined : { type: 'seq', items: values, flow: true };
}

/** The `when` block of a signal rule as a flow mapping. */
export function signalWhenNode(when: SignalWhenModel): YamlNode {
  const uriValue = when.uriValue.trim();
  return flowMap([
    ['http_status', statusNode(when)],
    ['business_code', optionalList(when.businessCode)],
    ['error_kind', optionalList(when.errorKind)],
    ['markers', optionalList(when.markers)],
    [
      'uri',
      when.uriMode !== 'any' && uriValue !== '' ? flowMap([[when.uriMode, text(uriValue)]]) : undefined,
    ],
    ['method', optionalList(when.method.map((m) => m.toUpperCase()))],
    ['latency_ms', rangeNode(when.latencyMs)],
    ['response_bytes', rangeNode(when.responseBytes)],
  ]);
}

/** One-line summary of a rule condition, e.g. "{ http_status: [429] }". */
export function signalWhenSummary(when: SignalWhenModel): string {
  return flowText(signalWhenNode(when));
}

function ruleNode(rule: SignalRuleModel): YamlNode {
  return blockMap([
    ['name', text(rule.name)],
    ['when', signalWhenNode(rule.when)],
    ['outcome', text(rule.outcome)],
    ['blame', text(rule.blame)],
  ]);
}

/** Generates signal policy YAML from the form model. */
export function signalToYaml(m: SignalModel): string {
  return emitYaml(
    blockMap([
      ...metaEntries(m),
      ['extends', text(m.extends)],
      ['trust_outcome_hint', bool(m.trustOutcomeHint)],
      ['rules', m.rules.length === 0 ? scalarSeq([]) : blockSeq(m.rules.map(ruleNode))],
    ]),
  );
}
