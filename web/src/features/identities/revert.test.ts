import { create } from '@bufbuild/protobuf';
import { timestampMs } from '@bufbuild/protobuf/wkt';
import { describe, expect, it } from 'vitest';

import { RevertActionsRequestSchema } from '@/gen/spinneret/v1/identity_admin_pb';

import { buildRevertRequest, defaultRevertForm, validateRevertForm } from './revert';

describe('buildRevertRequest', () => {
  it('builds a dry run with a start-only range and every action', () => {
    const start = new Date(2026, 8, 1, 10, 0).getTime();
    const form = {
      ...defaultRevertForm(start + 3_600_000, 'shop'),
      rule: ' captcha-ban ',
      policyId: 'pol_1',
    };
    const req = buildRevertRequest('default', form, true);
    expect(req).toMatchObject({
      namespace: 'default',
      site: 'shop',
      policyId: 'pol_1',
      rule: 'captcha-ban',
      actions: [],
      resetFailures: false,
      resetHealth: false,
      dryRun: true,
    });
    const msg = create(RevertActionsRequestSchema, req);
    expect(msg.timeRange?.start && timestampMs(msg.timeRange.start)).toBe(start);
    expect(msg.timeRange?.end).toBeUndefined();
  });

  it('orders actions canonically and includes the end', () => {
    const form = {
      ...defaultRevertForm(Date.now()),
      actions: ['cooldown', 'ban'] as const,
      start: '2026-09-01T10:00',
      end: '2026-09-01T12:30',
      resetHealth: true,
    };
    const req = buildRevertRequest('ns', { ...form, actions: [...form.actions] }, false);
    expect(req.actions).toEqual(['ban', 'cooldown']);
    const msg = create(RevertActionsRequestSchema, req);
    expect(msg.timeRange?.end && timestampMs(msg.timeRange.end)).toBe(new Date(2026, 8, 1, 12, 30).getTime());
    expect(req.dryRun).toBe(false);
    expect(req.resetHealth).toBe(true);
  });

  it('validates the form and refuses a missing start', () => {
    const form = { ...defaultRevertForm(Date.now()), start: '' };
    expect(validateRevertForm(form)).toEqual({ timeRange: 'startRequired' });
    expect(() => buildRevertRequest('ns', form, true)).toThrow();
    expect(validateRevertForm({ ...form, start: '2026-09-01T10:00', rule: 'x'.repeat(129) })).toEqual({
      rule: 'tooLong',
    });
  });
});
