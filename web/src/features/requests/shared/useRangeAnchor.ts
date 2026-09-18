import { useCallback, useRef } from 'react';

/**
 * Anchors relative time ranges per query: fetching the first page (empty page
 * token) records "now" for the filter key and later pages reuse it, so every
 * page of a result set covers the same window while refreshing the first page
 * moves the window forward. Call the returned function inside a queryFn.
 */
export function useRangeAnchor(): (filterKey: string, pageToken: string) => number {
  const anchors = useRef(new Map<string, number>());
  return useCallback((filterKey: string, pageToken: string) => {
    const known = anchors.current.get(filterKey);
    if (pageToken !== '' && known !== undefined) return known;
    const now = Date.now();
    anchors.current.set(filterKey, now);
    return now;
  }, []);
}
