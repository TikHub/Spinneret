import { create } from '@bufbuild/protobuf';
import { describe, expect, it } from 'vitest';

import { GetKEKStatusResponseSchema, KEKInfoSchema } from '@/gen/spinneret/v1/secret_admin_pb';

import { kekProgress, missingKeks, pendingRewrapRecords } from './kek';

describe('KEK status helpers', () => {
  const status = create(GetKEKStatusResponseSchema, {
    currentKekId: 'k2',
    keks: [
      create(KEKInfoSchema, { id: 'k1', current: false, configured: true, wrappedRecords: 120n }),
      create(KEKInfoSchema, { id: 'k0', current: false, configured: false, wrappedRecords: 3n }),
      create(KEKInfoSchema, { id: 'kx', current: false, configured: false, wrappedRecords: 0n }),
      create(KEKInfoSchema, { id: 'k2', current: true, configured: true, wrappedRecords: 900n }),
    ],
    rewrapRunning: true,
    rewrapDone: 25n,
    rewrapTotal: 100n,
  });

  it('counts records not on the current KEK', () => {
    expect(pendingRewrapRecords(status)).toBe(123);
  });

  it('lists unconfigured KEKs that still wrap records', () => {
    expect(missingKeks(status).map((k) => k.id)).toEqual(['k0']);
  });

  it('computes rewrap progress', () => {
    expect(kekProgress(status)).toEqual({ done: 25, total: 100, ratio: 0.25 });
    expect(kekProgress(create(GetKEKStatusResponseSchema, { rewrapRunning: true }))).toEqual({
      done: 0,
      total: 0,
      ratio: 0,
    });
    expect(kekProgress(create(GetKEKStatusResponseSchema, { rewrapDone: 5n, rewrapTotal: 4n })).ratio).toBe(
      1,
    );
  });
});
