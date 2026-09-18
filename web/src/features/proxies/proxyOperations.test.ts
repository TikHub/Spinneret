import { create } from '@bufbuild/protobuf';
import { describe, expect, it } from 'vitest';

import { BulkFailureSchema, BulkResultSchema } from '@/gen/spinneret/v1/common_pb';

import {
  buildOperateRequest,
  EMPTY_OPERATION_FORM,
  MAX_BULK_IDS,
  operationsForState,
  requiresTypedConfirmation,
  summarizeBulkResult,
  uniqueIds,
  validateOperation,
} from './proxyOperations';

describe('buildOperateRequest', () => {
  it('keeps site and duration for a site-scoped cooldown', () => {
    const req = buildOperateRequest('cooldown', ['pxy_1', 'pxy_2'], {
      site: ' shop ',
      duration: ' 30m ',
      reason: '  captcha storm ',
    });
    expect(req).toEqual({
      ids: ['pxy_1', 'pxy_2'],
      operation: 'cooldown',
      site: 'shop',
      duration: '30m',
      reason: 'captcha storm',
    });
  });

  it('clears fields the operation does not use', () => {
    const req = buildOperateRequest('disable', ['pxy_1'], { site: 'shop', duration: '1h', reason: '' });
    expect(req.site).toBe('');
    expect(req.duration).toBe('');
  });

  it('drops the site for ban and normalizes permanent', () => {
    const req = buildOperateRequest('ban', ['pxy_1'], {
      site: 'shop',
      duration: 'PERMANENT',
      reason: 'abuse',
    });
    expect(req).toMatchObject({ site: '', duration: 'permanent', operation: 'ban' });
  });

  it('deduplicates IDs and keeps order', () => {
    expect(buildOperateRequest('enable', ['b', 'a', 'b', ''], EMPTY_OPERATION_FORM).ids).toEqual(['b', 'a']);
    expect(uniqueIds(['x', 'x'])).toEqual(['x']);
  });

  it('allows an empty quarantine duration (policy default) and site-scoped reset_stats', () => {
    expect(buildOperateRequest('quarantine', ['p'], EMPTY_OPERATION_FORM).duration).toBe('');
    expect(buildOperateRequest('reset_stats', ['p'], { ...EMPTY_OPERATION_FORM, site: 'a' }).site).toBe('a');
  });
});

describe('validateOperation', () => {
  it('requires a positive duration for cooldown and ban', () => {
    expect(validateOperation('cooldown', ['p'], EMPTY_OPERATION_FORM)).toEqual(['duration_required']);
    expect(validateOperation('ban', ['p'], { ...EMPTY_OPERATION_FORM, duration: '0s' })).toEqual([
      'duration_zero',
    ]);
    expect(validateOperation('ban', ['p'], { ...EMPTY_OPERATION_FORM, duration: '7d' })).toEqual([]);
  });

  it('accepts permanent only for ban', () => {
    const form = { ...EMPTY_OPERATION_FORM, duration: 'permanent' };
    expect(validateOperation('ban', ['p'], form)).toEqual([]);
    expect(validateOperation('cooldown', ['p'], form)).toEqual(['duration_permanent']);
    expect(validateOperation('quarantine', ['p'], form)).toEqual(['duration_permanent']);
  });

  it('rejects malformed durations and long reasons', () => {
    expect(validateOperation('cooldown', ['p'], { ...EMPTY_OPERATION_FORM, duration: '10 minutes' })).toEqual(
      ['duration_invalid'],
    );
    expect(validateOperation('enable', ['p'], { ...EMPTY_OPERATION_FORM, reason: 'x'.repeat(513) })).toEqual([
      'reason_too_long',
    ]);
  });

  it('validates the ID count', () => {
    expect(validateOperation('enable', [], EMPTY_OPERATION_FORM)).toEqual(['no_ids']);
    const many = Array.from({ length: MAX_BULK_IDS + 1 }, (_, i) => `pxy_${i}`);
    expect(validateOperation('enable', many, EMPTY_OPERATION_FORM)).toEqual(['too_many_ids']);
  });
});

describe('requiresTypedConfirmation', () => {
  it('asks for archive and permanent bans only', () => {
    expect(requiresTypedConfirmation('archive', EMPTY_OPERATION_FORM)).toBe(true);
    expect(requiresTypedConfirmation('ban', { ...EMPTY_OPERATION_FORM, duration: 'permanent' })).toBe(true);
    expect(requiresTypedConfirmation('ban', { ...EMPTY_OPERATION_FORM, duration: '1d' })).toBe(false);
    expect(requiresTypedConfirmation('disable', EMPTY_OPERATION_FORM)).toBe(false);
  });
});

describe('summarizeBulkResult', () => {
  it('counts failures', () => {
    const result = create(BulkResultSchema, {
      matched: 3,
      succeeded: 2,
      failed: [create(BulkFailureSchema, { id: 'p3', reason: 'not_found', message: 'gone' })],
    });
    expect(summarizeBulkResult(result, 3)).toEqual({ matched: 3, succeeded: 2, failed: 1 });
    expect(summarizeBulkResult(undefined, 4)).toEqual({ matched: 4, succeeded: 4, failed: 0 });
  });
});

describe('operationsForState', () => {
  it('offers the transitions the server accepts for each state', () => {
    expect(operationsForState('active')).toEqual([
      'disable',
      'cooldown',
      'ban',
      'quarantine',
      'reset_stats',
      'archive',
    ]);
    // A dead proxy can be enabled (revived) as well as disabled.
    expect(operationsForState('dead')).toEqual([
      'enable',
      'disable',
      'cooldown',
      'ban',
      'quarantine',
      'reset_stats',
      'archive',
    ]);
    expect(operationsForState('disabled')).toEqual([
      'enable',
      'cooldown',
      'ban',
      'quarantine',
      'reset_stats',
      'archive',
    ]);
    // Banned proxies cannot be quarantined.
    expect(operationsForState('banned')).toEqual(['disable', 'cooldown', 'unban', 'reset_stats', 'archive']);
    expect(operationsForState('quarantined')).toContain('unquarantine');
    expect(operationsForState('quarantined')).not.toContain('quarantine');
    expect(operationsForState('retired')).toEqual(['restore', 'reset_stats']);
  });
});
