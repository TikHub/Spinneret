import { useInfiniteQuery } from '@tanstack/react-query';
import { HistoryIcon, LoaderCircleIcon, RefreshCwIcon } from 'lucide-react';
import { useEffect, useRef } from 'react';
import { useTranslation } from 'react-i18next';

import { useAuth, useScopedQueryKey } from '@/app/auth/AuthContext';
import { EmptyState } from '@/components/EmptyState';
import { ErrorState } from '@/components/ErrorState';
import { Button } from '@/components/ui/button';
import { Card, CardAction, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import { identityClient } from '@/lib/clients';

import { StateEventItem } from './StateEventItem';

const EVENTS_PAGE_SIZE = 30;

/** Lifecycle event timeline of an identity (ListStateEvents), newest first, loading more on scroll. */
export function StateEventTimeline({ identityId }: { identityId: string }) {
  const { t } = useTranslation('identities');
  const { namespaceName } = useAuth();
  const key = useScopedQueryKey();
  const scrollRef = useRef<HTMLDivElement>(null);
  const sentinelRef = useRef<HTMLDivElement>(null);

  const query = useInfiniteQuery({
    queryKey: key('identities', 'state-events', identityId),
    queryFn: ({ pageParam, signal }) =>
      identityClient.listStateEvents(
        {
          namespace: namespaceName ?? '',
          subjectKind: 'identity',
          subjectId: identityId,
          pageSize: EVENTS_PAGE_SIZE,
          pageToken: pageParam,
        },
        { signal },
      ),
    initialPageParam: '',
    getNextPageParam: (last) => last.nextPageToken || undefined,
    enabled: Boolean(namespaceName),
  });

  const { hasNextPage, isFetchingNextPage, fetchNextPage } = query;
  useEffect(() => {
    const root = scrollRef.current;
    const target = sentinelRef.current;
    if (!root || !target || !hasNextPage || typeof IntersectionObserver === 'undefined') return undefined;
    const observer = new IntersectionObserver(
      (entries) => {
        if (entries.some((e) => e.isIntersecting) && !isFetchingNextPage) void fetchNextPage();
      },
      { root, rootMargin: '120px' },
    );
    observer.observe(target);
    return () => observer.disconnect();
  }, [hasNextPage, isFetchingNextPage, fetchNextPage]);

  const events = query.data?.pages.flatMap((page) => page.events) ?? [];

  return (
    <Card>
      <CardHeader className="flex-row items-center">
        <CardTitle>{t('events.title')}</CardTitle>
        <CardAction>
          <Button
            variant="ghost"
            size="icon-sm"
            onClick={() => void query.refetch()}
            aria-label={t('common:actions.refresh')}
          >
            <RefreshCwIcon className={query.isFetching && !isFetchingNextPage ? 'animate-spin' : undefined} />
          </Button>
        </CardAction>
      </CardHeader>
      <CardContent>
        {query.isLoading ? (
          <div className="grid gap-3">
            {Array.from({ length: 4 }, (_, i) => (
              <Skeleton key={i} className="h-12 w-full" />
            ))}
          </div>
        ) : query.isError && events.length === 0 ? (
          <ErrorState compact error={query.error} onRetry={() => void query.refetch()} />
        ) : events.length === 0 ? (
          <EmptyState compact icon={HistoryIcon} title={t('events.empty')} />
        ) : (
          <div ref={scrollRef} className="max-h-[32rem] overflow-y-auto pr-1 pl-1.5">
            <ol aria-label={t('events.title')}>
              {events.map((event) => (
                <StateEventItem key={event.id} event={event} />
              ))}
            </ol>
            <div ref={sentinelRef} className="flex justify-center py-2">
              {isFetchingNextPage ? (
                <LoaderCircleIcon
                  className="size-4 animate-spin text-muted-foreground"
                  aria-label={t('common:table.loading')}
                />
              ) : hasNextPage ? (
                <Button variant="ghost" size="sm" onClick={() => void fetchNextPage()}>
                  {t('events.loadMore')}
                </Button>
              ) : (
                <span className="text-xs text-muted-foreground">{t('events.end')}</span>
              )}
            </div>
          </div>
        )}
      </CardContent>
    </Card>
  );
}
