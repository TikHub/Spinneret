import { act, renderHook } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import {
  currentPageIndex,
  currentPageToken,
  cursorReducer,
  initialCursorState,
  toResetKey,
  useCursorPagination,
} from './pagination';

describe('cursorReducer', () => {
  const key = 'k';

  it('starts on the first page', () => {
    const state = initialCursorState(50, key);
    expect(currentPageToken(state)).toBe('');
    expect(currentPageIndex(state)).toBe(0);
  });

  it('pushes next tokens and pops on previous', () => {
    let state = initialCursorState(50, key);
    state = cursorReducer(state, { type: 'next', nextPageToken: 'p2', resetKey: key });
    state = cursorReducer(state, { type: 'next', nextPageToken: 'p3', resetKey: key });
    expect(state.tokens).toEqual(['', 'p2', 'p3']);
    expect(currentPageIndex(state)).toBe(2);
    state = cursorReducer(state, { type: 'previous', resetKey: key });
    expect(currentPageToken(state)).toBe('p2');
    state = cursorReducer(state, { type: 'previous', resetKey: key });
    state = cursorReducer(state, { type: 'previous', resetKey: key });
    expect(state.tokens).toEqual(['']);
  });

  it('ignores empty and repeated next tokens', () => {
    let state = initialCursorState(50, key);
    const same = cursorReducer(state, { type: 'next', nextPageToken: '', resetKey: key });
    expect(same).toBe(state);
    state = cursorReducer(state, { type: 'next', nextPageToken: 'p2', resetKey: key });
    const repeated = cursorReducer(state, { type: 'next', nextPageToken: 'p2', resetKey: key });
    expect(repeated.tokens).toEqual(['', 'p2']);
    expect(cursorReducer(state, { type: 'next', nextPageToken: undefined, resetKey: key })).toBe(state);
  });

  it('returns to the first page on page size changes and first', () => {
    let state = initialCursorState(50, key);
    state = cursorReducer(state, { type: 'next', nextPageToken: 'p2', resetKey: key });
    const resized = cursorReducer(state, { type: 'setPageSize', pageSize: 100, resetKey: key });
    expect(resized).toMatchObject({ tokens: [''], pageSize: 100 });
    expect(cursorReducer(state, { type: 'setPageSize', pageSize: 0, resetKey: key })).toBe(state);
    expect(cursorReducer(state, { type: 'first', resetKey: key }).tokens).toEqual(['']);
  });

  it('discards the stack when the reset key changed', () => {
    let state = initialCursorState(25, 'a');
    state = cursorReducer(state, { type: 'next', nextPageToken: 'p2', resetKey: 'a' });
    const next = cursorReducer(state, { type: 'next', nextPageToken: 'q2', resetKey: 'b' });
    expect(next).toEqual({ tokens: ['', 'q2'], pageSize: 25, resetKey: 'b' });
  });
});

describe('toResetKey', () => {
  it('serializes values including bigint', () => {
    expect(toResetKey(undefined)).toBe('');
    expect(toResetKey({ site: 'a', n: 1n })).toBe('{"site":"a","n":"1"}');
  });
});

describe('useCursorPagination', () => {
  it('navigates and resets when filters change', () => {
    const { result, rerender } = renderHook(
      ({ filters }) => useCursorPagination({ pageSize: 10, resetOn: filters }),
      {
        initialProps: { filters: { state: 'active' } },
      },
    );
    expect(result.current).toMatchObject({ pageToken: '', pageIndex: 0, pageSize: 10, canPrevious: false });

    act(() => result.current.next('t2'));
    expect(result.current).toMatchObject({ pageToken: 't2', pageIndex: 1, canPrevious: true });

    rerender({ filters: { state: 'banned' } });
    expect(result.current).toMatchObject({ pageToken: '', pageIndex: 0, canPrevious: false });

    act(() => result.current.next('b2'));
    act(() => result.current.next('b3'));
    expect(result.current.pageIndex).toBe(2);
    act(() => result.current.previous());
    expect(result.current.pageToken).toBe('b2');
    act(() => result.current.setPageSize(50));
    expect(result.current).toMatchObject({ pageToken: '', pageSize: 50 });
  });
});
