import { IdText } from '@/components/CopyButton';
import { type DataTableColumn } from '@/components/data-table';
import { type RequestEvent } from '@/gen/spinneret/v1/dashboard_pb';
import { type Translate } from '@/lib/errors';
import { formatBytes, formatLatency, toNumber } from '@/lib/format';

import {
  EventFlags,
  EventTime,
  HttpStatus,
  IdentityLink,
  MarkerList,
  MonoText,
  OutcomeBadge,
} from '../shared/EventCells';

/** Columns hidden until enabled in the column menu. */
export const REQUEST_HIDDEN_COLUMNS = {
  request: false,
  blame: false,
  rule: false,
  identityType: false,
  receivedAt: false,
  outcomeHint: false,
  token: false,
};

/** Column definitions of the request explorer table (`t` from the requests namespace). */
export function requestColumns(t: Translate): DataTableColumn<RequestEvent>[] {
  const label = (key: string) => ({ label: t(`columns.${key}`) });
  const columns: DataTableColumn<RequestEvent>[] = [
    {
      id: 'time',
      header: t('columns.time'),
      enableHiding: false,
      cell: ({ row }) => <EventTime value={row.original.eventTime} />,
    },
    {
      id: 'target',
      header: t('columns.target'),
      meta: label('target'),
      cell: ({ row }) => (
        <span className="inline-flex flex-col leading-tight">
          <span className="text-xs font-medium">{row.original.site || '—'}</span>
          <span className="font-mono text-[11px] text-muted-foreground">
            {row.original.endpointGroup
              ? `${row.original.client}/${row.original.endpointGroup}`
              : t('shared.unknownGroup')}
          </span>
        </span>
      ),
    },
    {
      id: 'outcome',
      accessorKey: 'outcome',
      header: t('columns.outcome'),
      meta: label('outcome'),
      cell: ({ row }) => <OutcomeBadge outcome={row.original.outcome} />,
    },
    {
      id: 'httpStatus',
      accessorKey: 'httpStatus',
      header: t('columns.httpStatus'),
      meta: { ...label('httpStatus'), align: 'right' },
      cell: ({ row }) => <HttpStatus status={row.original.httpStatus} />,
    },
    {
      id: 'businessCode',
      accessorKey: 'businessCode',
      header: t('columns.businessCode'),
      meta: label('businessCode'),
      cell: ({ row }) => <MonoText value={row.original.businessCode} />,
    },
    {
      id: 'errorKind',
      accessorKey: 'errorKind',
      header: t('columns.errorKind'),
      meta: label('errorKind'),
      cell: ({ row }) => <MonoText value={row.original.errorKind} />,
    },
    {
      id: 'markers',
      header: t('columns.markers'),
      meta: label('markers'),
      cell: ({ row }) => <MarkerList markers={row.original.markers} />,
    },
    {
      id: 'latency',
      accessorKey: 'latencyMs',
      header: t('columns.latency'),
      meta: { ...label('latency'), align: 'right', className: 'tabular' },
      cell: ({ row }) => formatLatency(row.original.latencyMs),
    },
    {
      id: 'bytes',
      accessorFn: (e) => toNumber(e.responseBytes),
      header: t('columns.bytes'),
      meta: { ...label('bytes'), align: 'right', className: 'tabular' },
      cell: ({ row }) => formatBytes(row.original.responseBytes),
    },
    {
      id: 'identity',
      header: t('columns.identity'),
      meta: label('identity'),
      cell: ({ row }) => <IdentityLink id={row.original.identityId} site={row.original.site} truncate={16} />,
    },
    {
      id: 'proxy',
      header: t('columns.proxy'),
      meta: label('proxy'),
      cell: ({ row }) => <IdText value={row.original.proxyId} truncate={16} />,
    },
    {
      id: 'node',
      accessorKey: 'node',
      header: t('columns.node'),
      meta: label('node'),
      cell: ({ row }) => <MonoText value={row.original.node} />,
    },
    {
      id: 'lease',
      header: t('columns.lease'),
      meta: label('lease'),
      cell: ({ row }) => <IdText value={row.original.leaseId} truncate={14} />,
    },
    {
      id: 'report',
      header: t('columns.report'),
      meta: label('report'),
      cell: ({ row }) => <IdText value={row.original.reportId} truncate={14} />,
    },
    {
      id: 'flags',
      header: t('columns.flags'),
      meta: label('flags'),
      cell: ({ row }) => <EventFlags event={row.original} />,
    },
    {
      id: 'request',
      header: t('columns.request'),
      meta: { ...label('request'), className: 'max-w-80 truncate' },
      cell: ({ row }) => (
        <span className="font-mono text-xs" title={row.original.uri}>
          {row.original.method} {row.original.uri}
        </span>
      ),
    },
    {
      id: 'blame',
      accessorKey: 'blame',
      header: t('columns.blame'),
      meta: label('blame'),
      cell: ({ row }) => <MonoText value={row.original.blame} />,
    },
    {
      id: 'rule',
      accessorKey: 'rule',
      header: t('columns.rule'),
      meta: label('rule'),
      cell: ({ row }) => <MonoText value={row.original.rule} />,
    },
    {
      id: 'identityType',
      accessorKey: 'identityType',
      header: t('columns.identityType'),
      meta: label('identityType'),
      cell: ({ row }) => <MonoText value={row.original.identityType} />,
    },
    {
      id: 'receivedAt',
      header: t('columns.receivedAt'),
      meta: label('receivedAt'),
      cell: ({ row }) => <EventTime value={row.original.receivedAt} />,
    },
    {
      id: 'outcomeHint',
      accessorKey: 'outcomeHint',
      header: t('columns.outcomeHint'),
      meta: label('outcomeHint'),
      cell: ({ row }) => <MonoText value={row.original.outcomeHint} />,
    },
    {
      id: 'token',
      header: t('columns.token'),
      meta: label('token'),
      cell: ({ row }) => <IdText value={row.original.tokenId} truncate={14} />,
    },
  ];
  // Events come newest first from the server; sorting one page client-side would mislead.
  return columns.map((column) => ({ ...column, enableSorting: false }));
}
