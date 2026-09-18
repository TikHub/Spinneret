import { IdText } from '@/components/CopyButton';
import { type DataTableColumn } from '@/components/data-table';
import {
  EventTime,
  HttpStatus,
  IdentityLink,
  MarkerList,
  MonoText,
  OutcomeBadge,
} from '@/features/requests/shared/EventCells';
import { type RiskEvent } from '@/gen/spinneret/v1/dashboard_pb';
import { type Translate } from '@/lib/errors';
import { formatLatency } from '@/lib/format';
import { cn } from '@/lib/utils';

const BLAME_CLASS: Record<string, string> = {
  identity: 'text-rose-700 dark:text-rose-400',
  proxy: 'text-sky-700 dark:text-sky-400',
  both: 'text-orange-700 dark:text-orange-400',
};

/** Columns hidden until enabled in the column menu. */
export const RISK_HIDDEN_COLUMNS = { lease: false, report: false };

/** Column definitions of the risk event table (`t` from the risk-events namespace). */
export function riskColumns(t: Translate): DataTableColumn<RiskEvent>[] {
  const meta = (key: string, extra?: { align?: 'right'; className?: string }) => ({
    label: t(`columns.${key}`),
    ...extra,
  });
  return [
    {
      id: 'time',
      header: t('columns.time'),
      enableHiding: false,
      cell: ({ row }) => <EventTime value={row.original.createdAt} />,
    },
    {
      id: 'site',
      header: t('columns.site'),
      meta: meta('site'),
      cell: ({ row }) => <span className="text-xs font-medium">{row.original.site || '—'}</span>,
    },
    {
      id: 'group',
      header: t('columns.group'),
      meta: meta('group'),
      cell: ({ row }) =>
        row.original.endpointGroup ? (
          <span className="font-mono text-xs" title={row.original.endpointGroupId}>
            {row.original.client}/{row.original.endpointGroup}
          </span>
        ) : (
          <span className="text-xs text-muted-foreground">{t('unknownGroup')}</span>
        ),
    },
    {
      id: 'outcome',
      header: t('columns.outcome'),
      meta: meta('outcome'),
      cell: ({ row }) => <OutcomeBadge outcome={row.original.outcome} />,
    },
    {
      id: 'blame',
      header: t('columns.blame'),
      meta: meta('blame'),
      cell: ({ row }) => {
        const blame = row.original.blame;
        if (!blame) return <MonoText value="" />;
        return (
          <span className={cn('text-xs font-medium', BLAME_CLASS[blame] ?? 'text-muted-foreground')}>
            {t(`blame.${blame}`, { defaultValue: blame })}
          </span>
        );
      },
    },
    {
      id: 'rule',
      header: t('columns.rule'),
      meta: meta('rule'),
      cell: ({ row }) => <MonoText value={row.original.rule} />,
    },
    {
      id: 'httpStatus',
      header: t('columns.httpStatus'),
      meta: meta('httpStatus', { align: 'right' }),
      cell: ({ row }) => <HttpStatus status={row.original.httpStatus} />,
    },
    {
      id: 'businessCode',
      header: t('columns.businessCode'),
      meta: meta('businessCode'),
      cell: ({ row }) => <MonoText value={row.original.businessCode} />,
    },
    {
      id: 'errorKind',
      header: t('columns.errorKind'),
      meta: meta('errorKind'),
      cell: ({ row }) => <MonoText value={row.original.errorKind} />,
    },
    {
      id: 'markers',
      header: t('columns.markers'),
      meta: meta('markers'),
      cell: ({ row }) => <MarkerList markers={row.original.markers} />,
    },
    {
      id: 'latency',
      header: t('columns.latency'),
      meta: meta('latency', { align: 'right', className: 'tabular' }),
      cell: ({ row }) => formatLatency(row.original.latencyMs),
    },
    {
      id: 'identity',
      header: t('columns.identity'),
      meta: meta('identity'),
      cell: ({ row }) => <IdentityLink id={row.original.identityId} site={row.original.site} truncate={16} />,
    },
    {
      id: 'proxy',
      header: t('columns.proxy'),
      meta: meta('proxy'),
      cell: ({ row }) => <IdText value={row.original.proxyId} truncate={16} />,
    },
    {
      id: 'node',
      header: t('columns.node'),
      meta: meta('node'),
      cell: ({ row }) => <MonoText value={row.original.node} />,
    },
    {
      id: 'lease',
      header: t('columns.lease'),
      meta: meta('lease'),
      cell: ({ row }) => <IdText value={row.original.leaseId} truncate={14} />,
    },
    {
      id: 'report',
      header: t('columns.report'),
      meta: meta('report'),
      cell: ({ row }) => <IdText value={row.original.reportId} truncate={14} />,
    },
  ];
}
