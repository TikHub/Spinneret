import { createInstance, type TFunction } from 'i18next';
import { beforeAll, describe, expect, it } from 'vitest';

import enCommon from '@/i18n/locales/en/common.json';
import zhCommon from '@/i18n/locales/zh-CN/common.json';

import {
  DURATION_PATTERN,
  durationToMs,
  formatDuration,
  humanizeDuration,
  isValidDuration,
  normalizeDuration,
  parseDuration,
  validateDurationInput,
} from './duration';

const SECOND = 1000;
const MINUTE = 60 * SECOND;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

describe('parseDuration', () => {
  it.each([
    ['', 0],
    ['0', 0],
    ['500ms', 500],
    ['30s', 30 * SECOND],
    ['10m', 10 * MINUTE],
    ['24h', 24 * HOUR],
    ['7d', 7 * DAY],
    ['1h30m', HOUR + 30 * MINUTE],
    ['1d12h', DAY + 12 * HOUR],
    ['1.5h', 1.5 * HOUR],
    ['2m500ms', 2 * MINUTE + 500],
    ['  45s  ', 45 * SECOND],
  ])('parses %j as %d ms', (input, ms) => {
    expect(parseDuration(input)).toEqual({ permanent: false, ms });
  });

  it('parses the permanent keyword case-insensitively', () => {
    expect(parseDuration('permanent')).toEqual({ permanent: true, ms: -1 });
    expect(parseDuration('PERMANENT')).toEqual({ permanent: true, ms: -1 });
    expect(durationToMs('permanent')).toBe(-1);
  });

  it.each(['abc', '5', '-5s', '1h1d', '1.5d', 'd', '5x', '10 m', '1H', 'permanently', '5s5'])(
    'rejects %j',
    (input) => {
      expect(parseDuration(input)).toBeUndefined();
      expect(isValidDuration(input)).toBe(false);
    },
  );

  it('agrees with the admin API validation pattern on valid inputs', () => {
    for (const input of ['30s', '10m', '1h30m', '7d', '1d12h', '1.5h', '250ms', 'permanent', '0']) {
      expect(DURATION_PATTERN.test(input)).toBe(true);
      expect(parseDuration(input)).toBeDefined();
    }
  });
});

describe('formatDuration', () => {
  it.each([
    [0, '0s'],
    [250, '250ms'],
    [30 * SECOND, '30s'],
    [HOUR + 30 * MINUTE, '1h30m'],
    [DAY + 12 * HOUR, '1d12h'],
    [7 * DAY, '7d'],
    [DAY + HOUR + MINUTE + SECOND + 5, '1d1h1m1s5ms'],
    [-1, 'permanent'],
  ])('formats %d ms as %j', (ms, expected) => {
    expect(formatDuration(ms)).toBe(expected);
  });

  it('round-trips canonical strings', () => {
    for (const input of ['30s', '10m', '1h30m', '7d', '1d12h', '250ms', 'permanent']) {
      expect(normalizeDuration(input)).toBe(input);
    }
    expect(normalizeDuration('90m')).toBe('1h30m');
    expect(normalizeDuration('36h')).toBe('1d12h');
    expect(normalizeDuration('bogus')).toBeUndefined();
  });
});

describe('humanizeDuration', () => {
  it('keeps the largest units', () => {
    expect(humanizeDuration(DAY + 2 * HOUR + 3 * MINUTE)).toBe('1d 2h');
    expect(humanizeDuration(DAY + 2 * HOUR + 3 * MINUTE, 3)).toBe('1d 2h 3m');
    expect(humanizeDuration(3 * MINUTE + 20 * SECOND)).toBe('3m 20s');
    expect(humanizeDuration(250)).toBe('250ms');
    expect(humanizeDuration(0)).toBe('0s');
    expect(humanizeDuration(-1)).toBe('permanent');
  });

  it('accepts an options object', () => {
    expect(humanizeDuration(DAY + 2 * HOUR + 3 * MINUTE, { maxUnits: 3 })).toBe('1d 2h 3m');
    expect(humanizeDuration(DAY + 2 * HOUR + 3 * MINUTE, {})).toBe('1d 2h');
  });

  describe('localized', () => {
    let en: TFunction;
    let zh: TFunction;
    beforeAll(async () => {
      const i18n = createInstance();
      await i18n.init({
        lng: 'en',
        resources: { en: { common: enCommon }, 'zh-CN': { common: zhCommon } },
        defaultNS: 'common',
        interpolation: { escapeValue: false },
      });
      en = i18n.getFixedT('en');
      // A feature namespace's t still resolves the common keys.
      zh = i18n.getFixedT('zh-CN', 'identities');
    });

    it('translates units and the permanent keyword', () => {
      expect(humanizeDuration(DAY + 2 * HOUR + 3 * MINUTE, { t: zh })).toBe('1天 2小时');
      expect(humanizeDuration(3 * MINUTE + 20 * SECOND, { maxUnits: 3, t: zh })).toBe('3分钟 20秒');
      expect(humanizeDuration(250, { t: zh })).toBe('250毫秒');
      expect(humanizeDuration(0, { t: zh })).toBe('0秒');
      expect(humanizeDuration(-1, { t: zh })).toBe('永久');
    });

    it('matches the compact English form except for the permanent label', () => {
      for (const ms of [DAY + 2 * HOUR + 3 * MINUTE, 3 * MINUTE + 20 * SECOND, 250, 0, 45 * DAY]) {
        expect(humanizeDuration(ms, { t: en })).toBe(humanizeDuration(ms));
      }
      expect(humanizeDuration(DAY + 2 * HOUR + 3 * MINUTE, { maxUnits: 3, t: en })).toBe('1d 2h 3m');
      expect(humanizeDuration(-1, { t: en })).toBe('Permanent');
    });
  });
});

describe('validateDurationInput', () => {
  it('reports empty, invalid and permanent values', () => {
    expect(validateDurationInput('')).toBe('empty');
    expect(validateDurationInput('', { allowEmpty: true })).toBe('ok');
    expect(validateDurationInput('nope')).toBe('invalid');
    expect(validateDurationInput('permanent')).toBe('permanent');
    expect(validateDurationInput('permanent', { allowPermanent: true })).toBe('ok');
    expect(validateDurationInput('30m')).toBe('ok');
    expect(isValidDuration('permanent', false)).toBe(false);
  });
});
