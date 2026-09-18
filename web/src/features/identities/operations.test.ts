import { create } from '@bufbuild/protobuf';
import { describe, expect, it } from 'vitest';

import { BulkFailureSchema, BulkResultSchema } from '@/gen/spinneret/v1/common_pb';

import {
  availableOperations,
  buildBulkOperateRequest,
  buildOperateIdentitiesRequest,
  defaultOperationForm,
  requiresTypedConfirmation,
  summarizeBulkResult,
  validateOperationDuration,
  validateOperationForm,
  type OperationForm,
} from './operations';

function form(patch: Partial<OperationForm> & Pick<OperationForm, 'operation'>): OperationForm {
  return { ...defaultOperationForm(patch.operation), ...patch };
}

describe('buildOperateIdentitiesRequest', () => {
  it('builds a site cooldown and drops fields the operation ignores', () => {
    const req = buildOperateIdentitiesRequest(
      ['idt_1', 'idt_2', 'idt_1', ''],
      form({ operation: 'cooldown', duration: ' 45m ', reason: ' flaky ', resetHealth: true }),
    );
    expect(req).toEqual({
      ids: ['idt_1', 'idt_2'],
      operation: 'cooldown',
      scope: 'identity_site',
      endpointGroupId: '',
      duration: '45m',
      reason: 'flaky',
      resetFailures: false,
      resetHealth: false,
    });
  });

  it('keeps the endpoint group only for identity_endpoint', () => {
    const endpoint = buildOperateIdentitiesRequest(
      ['idt_1'],
      form({ operation: 'cooldown', scope: 'identity_endpoint', endpointGroupId: 'eg_1' }),
    );
    expect(endpoint.scope).toBe('identity_endpoint');
    expect(endpoint.endpointGroupId).toBe('eg_1');
    const site = buildOperateIdentitiesRequest(
      ['idt_1'],
      form({ operation: 'cooldown', scope: 'identity_site', endpointGroupId: 'eg_1' }),
    );
    expect(site.endpointGroupId).toBe('');
  });

  it('normalizes a permanent ban and passes reset flags for unban', () => {
    expect(
      buildOperateIdentitiesRequest(['a'], form({ operation: 'ban', duration: 'PERMANENT' })).duration,
    ).toBe('permanent');
    const unban = buildOperateIdentitiesRequest(
      ['a'],
      form({
        operation: 'unban',
        duration: '1h',
        scope: 'identity_endpoint',
        resetFailures: true,
        resetHealth: true,
      }),
    );
    expect(unban).toMatchObject({ duration: '', scope: '', resetFailures: true, resetHealth: true });
  });

  it('rejects empty and oversized ID lists', () => {
    expect(() => buildOperateIdentitiesRequest([], form({ operation: 'enable' }))).toThrow();
    const many = Array.from({ length: 1001 }, (_, i) => `idt_${i}`);
    expect(() => buildOperateIdentitiesRequest(many, form({ operation: 'enable' }))).toThrow();
  });
});

describe('buildBulkOperateRequest', () => {
  it('wraps the filter with dry run and server limit', () => {
    const req = buildBulkOperateRequest(
      'default',
      { site: 'shop', states: ['active'] },
      form({ operation: 'reset_stats', scope: '' }),
      true,
    );
    expect(req).toEqual({
      namespace: 'default',
      filter: { site: 'shop', states: ['active'] },
      operation: 'reset_stats',
      scope: '',
      endpointGroupId: '',
      duration: '',
      reason: '',
      resetFailures: false,
      resetHealth: false,
      dryRun: true,
      limit: 0,
    });
  });
});

describe('validation', () => {
  it('validates durations per rule', () => {
    expect(validateOperationDuration('', 'required', false)).toBe('required');
    expect(validateOperationDuration('0', 'required', false)).toBe('required');
    expect(validateOperationDuration('5x', 'required', false)).toBe('invalid');
    expect(validateOperationDuration('permanent', 'required', false)).toBe('permanentNotAllowed');
    expect(validateOperationDuration('permanent', 'required', true)).toBeUndefined();
    expect(validateOperationDuration('', 'optional', false)).toBeUndefined();
    expect(validateOperationDuration('0', 'optional', false)).toBe('invalid');
    expect(validateOperationDuration('garbage', 'none', false)).toBeUndefined();
  });

  it('requires an endpoint group for identity_endpoint and limits the reason', () => {
    expect(validateOperationForm(form({ operation: 'cooldown', scope: 'identity_endpoint' }))).toEqual({
      endpointGroupId: 'required',
    });
    expect(validateOperationForm(form({ operation: 'disable', reason: 'x'.repeat(513) }))).toEqual({
      reason: 'tooLong',
    });
    expect(validateOperationForm(form({ operation: 'quarantine', duration: 'permanent' }))).toEqual({
      duration: 'permanentNotAllowed',
    });
  });

  it('asks for typed confirmation for archive and permanent bans', () => {
    expect(requiresTypedConfirmation({ operation: 'archive', duration: '' })).toBe(true);
    expect(requiresTypedConfirmation({ operation: 'ban', duration: 'permanent' })).toBe(true);
    expect(requiresTypedConfirmation({ operation: 'ban', duration: '7d' })).toBe(false);
  });
});

describe('availableOperations', () => {
  it('offers state-appropriate operations', () => {
    expect(availableOperations('retired')).toEqual(['restore']);
    expect(availableOperations('banned')).toContain('unban');
    expect(availableOperations('banned')).not.toContain('ban');
    expect(availableOperations('disabled')).toContain('enable');
    expect(availableOperations('pending')).toContain('activate');
    expect(availableOperations('active')).not.toContain('activate');
  });

  it('matches the server transition table', () => {
    expect(availableOperations('expired')).toEqual(['cooldown', 'ban', 'disable', 'archive', 'reset_stats']);
    expect(availableOperations('disabled')).toEqual(['cooldown', 'ban', 'enable', 'archive', 'reset_stats']);
    expect(availableOperations('quarantined')).toEqual([
      'cooldown',
      'ban',
      'unquarantine',
      'expire',
      'disable',
      'archive',
      'reset_stats',
    ]);
    expect(availableOperations('banned')).toEqual(['cooldown', 'unban', 'disable', 'archive', 'reset_stats']);
    expect(availableOperations('active')).toEqual([
      'cooldown',
      'ban',
      'quarantine',
      'expire',
      'disable',
      'archive',
      'reset_stats',
    ]);
  });
});

describe('summarizeBulkResult', () => {
  it('classifies outcomes', () => {
    const failure = create(BulkFailureSchema, { id: 'idt_x', reason: 'not_found', message: 'gone' });
    expect(
      summarizeBulkResult(create(BulkResultSchema, { matched: 3, succeeded: 2, failed: [failure] })),
    ).toEqual({
      matched: 3,
      succeeded: 2,
      failed: 1,
      outcome: 'partial',
    });
    expect(summarizeBulkResult(create(BulkResultSchema, { succeeded: 2 })).outcome).toBe('success');
    expect(summarizeBulkResult(create(BulkResultSchema, { failed: [failure] })).outcome).toBe('failed');
    expect(summarizeBulkResult(undefined)).toEqual({ matched: 0, succeeded: 0, failed: 0, outcome: 'none' });
  });
});
