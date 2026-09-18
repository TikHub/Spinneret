import { type RevertMode, REVERT_MODES } from '../constants';
import { blockMap, emitYaml } from '../yaml/emit';
import {
  asObject,
  bool,
  checkKeys,
  META_KEYS,
  metaEntries,
  ModelReadError,
  num,
  readBool,
  readMeta,
  readText,
  section,
  text,
  type PolicyMetaModel,
  type YamlObject,
} from './common';

/** Form model of a breaker policy (spec section 7). */
export interface BreakerModel extends PolicyMetaModel {
  enabled: boolean;
  window: string;
  buckets: string;
  minRequests: string;
  trip: { riskRatioGte: string; distinctCaptchaIdentitiesGte: string; successRatioLte: string };
  openDuration: string;
  maxOpenDuration: string;
  resetOpenCountAfter: string;
  halfOpen: { probeLeasesPer10s: string; closeMinSamples: string; closeSuccessRatioGte: string };
  revertRecentCooldowns: RevertMode;
}

/** Server defaults, shown as placeholders for empty fields. */
export const BREAKER_DEFAULTS = {
  window: '60s',
  buckets: '12',
  minRequests: '50',
  riskRatioGte: '0.4',
  distinctCaptchaIdentitiesGte: '10',
  successRatioLte: '0.2',
  openDuration: '2m',
  maxOpenDuration: '1h',
  resetOpenCountAfter: '30m',
  probeLeasesPer10s: '5',
  closeMinSamples: '5',
  closeSuccessRatioGte: '0.8',
  revertRecentCooldowns: 'endpoint' as RevertMode,
} as const;

export function emptyBreakerModel(name = ''): BreakerModel {
  return {
    name,
    description: '',
    bind: null,
    enabled: true,
    window: '',
    buckets: '',
    minRequests: '',
    trip: { riskRatioGte: '', distinctCaptchaIdentitiesGte: '', successRatioLte: '' },
    openDuration: '',
    maxOpenDuration: '',
    resetOpenCountAfter: '',
    halfOpen: { probeLeasesPer10s: '', closeMinSamples: '', closeSuccessRatioGte: '' },
    revertRecentCooldowns: BREAKER_DEFAULTS.revertRecentCooldowns,
  };
}

/** Maps revert_recent_cooldowns (a mode or the legacy boolean) to a mode. */
export function readRevertMode(value: unknown): RevertMode {
  if (value === undefined || value === null) return BREAKER_DEFAULTS.revertRecentCooldowns;
  if (value === true) return 'endpoint';
  if (value === false) return 'none';
  if (typeof value === 'string' && (REVERT_MODES as readonly string[]).includes(value)) {
    return value as RevertMode;
  }
  throw new ModelReadError(`revert_recent_cooldowns must be one of ${REVERT_MODES.join('|')}`);
}

const BREAKER_KEYS = [
  ...META_KEYS,
  'enabled',
  'window',
  'buckets',
  'min_requests',
  'trip',
  'open_duration',
  'max_open_duration',
  'reset_open_count_after',
  'half_open',
  'revert_recent_cooldowns',
];

export function readBreakerModel(value: YamlObject): BreakerModel {
  checkKeys(value, BREAKER_KEYS, '');
  const trip = asObject(value.trip, 'trip');
  checkKeys(trip, ['risk_ratio_gte', 'distinct_captcha_identities_gte', 'success_ratio_lte'], 'trip');
  const half = asObject(value.half_open, 'half_open');
  checkKeys(half, ['probe_leases_per_10s', 'close_min_samples', 'close_success_ratio_gte'], 'half_open');
  return {
    ...readMeta(value),
    enabled: readBool(value, 'enabled', '', true),
    window: readText(value, 'window', ''),
    buckets: readText(value, 'buckets', ''),
    minRequests: readText(value, 'min_requests', ''),
    trip: {
      riskRatioGte: readText(trip, 'risk_ratio_gte', 'trip'),
      distinctCaptchaIdentitiesGte: readText(trip, 'distinct_captcha_identities_gte', 'trip'),
      successRatioLte: readText(trip, 'success_ratio_lte', 'trip'),
    },
    openDuration: readText(value, 'open_duration', ''),
    maxOpenDuration: readText(value, 'max_open_duration', ''),
    resetOpenCountAfter: readText(value, 'reset_open_count_after', ''),
    halfOpen: {
      probeLeasesPer10s: readText(half, 'probe_leases_per_10s', 'half_open'),
      closeMinSamples: readText(half, 'close_min_samples', 'half_open'),
      closeSuccessRatioGte: readText(half, 'close_success_ratio_gte', 'half_open'),
    },
    revertRecentCooldowns: readRevertMode(value.revert_recent_cooldowns),
  };
}

/** Generates breaker policy YAML from the form model. */
export function breakerToYaml(m: BreakerModel): string {
  return emitYaml(
    blockMap([
      ...metaEntries(m),
      ['enabled', bool(m.enabled)],
      ['window', text(m.window)],
      ['buckets', num(m.buckets)],
      ['min_requests', num(m.minRequests)],
      [
        'trip',
        section([
          ['risk_ratio_gte', num(m.trip.riskRatioGte)],
          ['distinct_captcha_identities_gte', num(m.trip.distinctCaptchaIdentitiesGte)],
          ['success_ratio_lte', num(m.trip.successRatioLte)],
        ]),
      ],
      ['open_duration', text(m.openDuration)],
      ['max_open_duration', text(m.maxOpenDuration)],
      ['reset_open_count_after', text(m.resetOpenCountAfter)],
      [
        'half_open',
        section([
          ['probe_leases_per_10s', num(m.halfOpen.probeLeasesPer10s)],
          ['close_min_samples', num(m.halfOpen.closeMinSamples)],
          ['close_success_ratio_gte', num(m.halfOpen.closeSuccessRatioGte)],
        ]),
      ],
      ['revert_recent_cooldowns', text(m.revertRecentCooldowns)],
    ]),
  );
}
