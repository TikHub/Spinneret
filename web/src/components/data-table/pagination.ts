import { useCallback, useMemo, useReducer } from 'react';

/** Page size options offered by the pagination controls. */
export const PAGE_SIZE_OPTIONS = [25, 50, 100, 200, 500] as const;
export const DEFAULT_PAGE_SIZE = 50;

/**
 * Cursor pagination state. `tokens` is a stack of page tokens for the pages
 * visited so far: tokens[0] is "" (first page) and the last entry is the token
 * of the current page. Going back pops the stack.
 */
export interface CursorPaginationState {
  tokens: readonly string[];
  pageSize: number;
  /** Serialized filters; a different key means "start over at the first page". */
  resetKey: string;
}

export type CursorPaginationAction =
  | { type: 'next'; nextPageToken: string | undefined; resetKey: string }
  | { type: 'previous'; resetKey: string }
  | { type: 'first'; resetKey: string }
  | { type: 'setPageSize'; pageSize: number; resetKey: string };

export function initialCursorState(
  pageSize: number = DEFAULT_PAGE_SIZE,
  resetKey = '',
): CursorPaginationState {
  return { tokens: [''], pageSize, resetKey };
}

/** Pure reducer; every action carries the current reset key so stale stacks are discarded first. */
export function cursorReducer(
  state: CursorPaginationState,
  action: CursorPaginationAction,
): CursorPaginationState {
  const base =
    state.resetKey === action.resetKey ? state : initialCursorState(state.pageSize, action.resetKey);
  switch (action.type) {
    case 'next': {
      const token = action.nextPageToken;
      // No next page, or a repeated click with the same (not yet loaded) token.
      if (!token || base.tokens.includes(token)) return base;
      return { ...base, tokens: [...base.tokens, token] };
    }
    case 'previous':
      return base.tokens.length <= 1 ? base : { ...base, tokens: base.tokens.slice(0, -1) };
    case 'first':
      return base.tokens.length === 1 ? base : { ...base, tokens: [''] };
    case 'setPageSize':
      if (!Number.isInteger(action.pageSize) || action.pageSize <= 0) return base;
      return action.pageSize === base.pageSize ? base : { ...base, pageSize: action.pageSize, tokens: [''] };
    default:
      return base;
  }
}

/** Token of the current page ("" for the first page). */
export function currentPageToken(state: CursorPaginationState): string {
  return state.tokens[state.tokens.length - 1] ?? '';
}

/** Zero-based index of the current page. */
export function currentPageIndex(state: CursorPaginationState): number {
  return state.tokens.length - 1;
}

/** Serializes filter values into a reset key (bigint-safe, stable for plain objects). */
export function toResetKey(value: unknown): string {
  if (value === undefined) return '';
  try {
    return JSON.stringify(value, (_k, v: unknown) => (typeof v === 'bigint' ? v.toString() : v)) ?? '';
  } catch {
    return String(value);
  }
}

/** Pagination handle passed to list requests and to DataTable. */
export interface CursorPagination {
  /** Send as `page_token`. */
  pageToken: string;
  /** Send as `page_size`. */
  pageSize: number;
  pageIndex: number;
  canPrevious: boolean;
  /** Moves to the page of `nextPageToken` (from the current response); no-op when empty. */
  next: (nextPageToken: string | undefined) => void;
  previous: () => void;
  first: () => void;
  setPageSize: (pageSize: number) => void;
}

export interface UseCursorPaginationOptions {
  pageSize?: number;
  /** Filters (any JSON-serializable value); changing them returns to the first page. */
  resetOn?: unknown;
}

/**
 * Cursor pagination with a page-token stack for back navigation.
 *
 *   const pager = useCursorPagination({ resetOn: filters });
 *   const keepPrevious = useScopedPlaceholder();
 *   useQuery({ queryKey: key('proxies', filters, pager.pageToken, pager.pageSize),
 *              queryFn: () => proxyClient.listProxies({ ...filters, pageSize: pager.pageSize, pageToken: pager.pageToken }),
 *              placeholderData: keepPrevious });
 *   <DataTable pagination={{ pager, nextPageToken: data?.nextPageToken, total: data?.total }} ... />
 */
export function useCursorPagination({
  pageSize = DEFAULT_PAGE_SIZE,
  resetOn,
}: UseCursorPaginationOptions = {}): CursorPagination {
  const resetKey = toResetKey(resetOn);
  const [stored, dispatch] = useReducer(cursorReducer, undefined, () =>
    initialCursorState(pageSize, resetKey),
  );
  const state = stored.resetKey === resetKey ? stored : initialCursorState(stored.pageSize, resetKey);

  const next = useCallback(
    (nextPageToken: string | undefined) => dispatch({ type: 'next', nextPageToken, resetKey }),
    [resetKey],
  );
  const previous = useCallback(() => dispatch({ type: 'previous', resetKey }), [resetKey]);
  const first = useCallback(() => dispatch({ type: 'first', resetKey }), [resetKey]);
  const setPageSize = useCallback(
    (size: number) => dispatch({ type: 'setPageSize', pageSize: size, resetKey }),
    [resetKey],
  );

  const pageToken = currentPageToken(state);
  const pageIndex = currentPageIndex(state);
  return useMemo(
    () => ({
      pageToken,
      pageSize: state.pageSize,
      pageIndex,
      canPrevious: pageIndex > 0,
      next,
      previous,
      first,
      setPageSize,
    }),
    [pageToken, state.pageSize, pageIndex, next, previous, first, setPageSize],
  );
}
