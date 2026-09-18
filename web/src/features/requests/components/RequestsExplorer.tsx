import { RefreshCwIcon } from 'lucide-react';
import { useCallback, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';

import { useAuth } from '@/app/auth/AuthContext';
import { LIVE_REFETCH_MS } from '@/app/queryClient';
import { DataTable } from '@/components/data-table';
import { EmptyState } from '@/components/EmptyState';
import { PageHeader } from '@/components/PageHeader';
import { Button } from '@/components/ui/button';
import { Label } from '@/components/ui/label';
import { Switch } from '@/components/ui/switch';
import { type RequestEvent } from '@/gen/spinneret/v1/dashboard_pb';

import { type RequestFilters } from '../requestFilters';
import { requestRowIds } from '../requestRows';
import { useRequestEvents } from '../useRequestEvents';
import { ClickHouseDisabled } from './ClickHouseDisabled';
import { REQUEST_HIDDEN_COLUMNS, requestColumns } from './requestColumns';
import { RequestEventSheet } from './RequestEventSheet';
import { RequestFiltersBar } from './RequestFiltersBar';
import { RequestSummary } from './RequestSummary';

export interface RequestsExplorerProps {
  filters: RequestFilters;
  onFiltersChange: (filters: RequestFilters) => void;
}

/** Request explorer over ClickHouse report events: filters, summary and paged table. */
export function RequestsExplorer({ filters, onFiltersChange }: RequestsExplorerProps) {
  const { t } = useTranslation('requests');
  const { namespaceName } = useAuth();
  const [autoRefresh, setAutoRefresh] = useState(false);
  const [selected, setSelected] = useState<RequestEvent | undefined>(undefined);
  const { pager, events, refresh, summary, summaryLoading, clickHouseDisabled } = useRequestEvents(filters, {
    autoRefresh,
    paused: selected !== undefined,
  });
  const columns = useMemo(() => requestColumns(t), [t]);
  const rows = events.data?.events;
  const rowIds = useMemo(() => requestRowIds(rows ?? []), [rows]);
  const getRowId = useCallback(
    (event: RequestEvent, index: number) => rowIds.get(event) ?? `row-${index}`,
    [rowIds],
  );

  if (!namespaceName) {
    return <EmptyState title={t('noNamespace')} />;
  }

  return (
    <>
      <PageHeader
        title={t('title')}
        description={t('description')}
        actions={
          <>
            <div className="flex items-center gap-2">
              <Switch
                id="requests-auto-refresh"
                checked={autoRefresh}
                onCheckedChange={setAutoRefresh}
                disabled={clickHouseDisabled}
              />
              <Label htmlFor="requests-auto-refresh" className="text-xs font-normal text-muted-foreground">
                {t('autoRefresh', { seconds: LIVE_REFETCH_MS / 1000 })}
              </Label>
            </div>
            <Button
              variant="outline"
              size="icon-sm"
              onClick={refresh}
              aria-label={t('common:actions.refresh')}
            >
              <RefreshCwIcon className={events.isFetching ? 'animate-spin' : undefined} />
            </Button>
          </>
        }
      />
      <div className="space-y-4">
        <RequestFiltersBar filters={filters} onChange={onFiltersChange} />
        {clickHouseDisabled ? (
          <ClickHouseDisabled error={events.error} onRetry={() => void events.refetch()} />
        ) : (
          <>
            {/* Without a summary a failed query shows only the table error, not "no events". */}
            {!(events.isError && summary === undefined) && (
              <RequestSummary summary={summary} loading={summaryLoading} />
            )}
            <DataTable
              columns={columns}
              data={rows}
              getRowId={getRowId}
              isLoading={events.isLoading}
              isFetching={events.isFetching}
              error={events.error ?? undefined}
              onRetry={() => void events.refetch()}
              emptyTitle={t('empty.title')}
              emptyDescription={t('empty.description')}
              initialColumnVisibility={REQUEST_HIDDEN_COLUMNS}
              onRowClick={setSelected}
              virtualize="auto"
              estimateRowHeight={41}
              maxHeight="70vh"
              pagination={{ pager, nextPageToken: events.data?.nextPageToken }}
            />
          </>
        )}
      </div>
      <RequestEventSheet
        event={selected}
        onOpenChange={(open) => {
          if (!open) setSelected(undefined);
        }}
      />
    </>
  );
}
