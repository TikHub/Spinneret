import { create } from '@bufbuild/protobuf';
import { describe, expect, it } from 'vitest';

import { RequestEventSchema } from '@/gen/spinneret/v1/dashboard_pb';

import { requestRowIds } from './requestRows';

describe('requestRowIds', () => {
  it('keeps IDs unique for duplicated and anonymous events', () => {
    const a = create(RequestEventSchema, { leaseId: 'lse_1', reportId: 'rpt_1' });
    const dup = create(RequestEventSchema, { leaseId: 'lse_1', reportId: 'rpt_1' });
    const anonymous = create(RequestEventSchema, {});
    const ids = requestRowIds([a, dup, anonymous]);
    expect(ids.get(a)).toBe('lse_1:rpt_1');
    expect(ids.get(dup)).toBe('lse_1:rpt_1#1');
    expect(ids.get(anonymous)).toBe('row-2');
    expect(new Set(ids.values()).size).toBe(3);
  });
});
