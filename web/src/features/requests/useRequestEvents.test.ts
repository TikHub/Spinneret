import { Code, ConnectError } from '@connectrpc/connect';
import { describe, expect, it } from 'vitest';

import { REASON_HEADER } from '@/lib/errors';

import { isClickHouseDisabled } from './useRequestEvents';

describe('isClickHouseDisabled', () => {
  it('recognizes the unavailable error of a server without ClickHouse', () => {
    const withReason = new ConnectError(
      'request events are unavailable: ClickHouse is not configured',
      Code.Unavailable,
      new Headers({ [REASON_HEADER]: 'failed_precondition' }),
    );
    expect(isClickHouseDisabled(withReason)).toBe(true);
    expect(isClickHouseDisabled(new ConnectError('clickhouse is not configured', Code.Unavailable))).toBe(
      true,
    );
  });

  it('treats other errors as ordinary failures', () => {
    expect(isClickHouseDisabled(new ConnectError('HTTP 503', Code.Unavailable))).toBe(false);
    expect(
      isClickHouseDisabled(
        new ConnectError('rebuilding', Code.Unavailable, new Headers({ [REASON_HEADER]: 'rebuilding' })),
      ),
    ).toBe(false);
    expect(isClickHouseDisabled(new ConnectError('ClickHouse timeout', Code.Internal))).toBe(false);
    expect(isClickHouseDisabled(undefined)).toBe(false);
  });
});
