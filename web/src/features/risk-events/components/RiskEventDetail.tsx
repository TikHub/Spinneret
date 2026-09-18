import { useTranslation } from 'react-i18next';

import { IdText } from '@/components/CopyButton';
import { DetailGrid } from '@/features/requests/shared/DetailGrid';
import { MonoText } from '@/features/requests/shared/EventCells';
import { type RiskEvent } from '@/gen/spinneret/v1/dashboard_pb';
import { formatBytes, formatLatency } from '@/lib/format';
import { formatDateTime, toDate } from '@/lib/time';

function durationText(event: RiskEvent): string {
  const started = toDate(event.startedAt);
  const finished = toDate(event.finishedAt);
  return started && finished ? formatLatency(finished.getTime() - started.getTime()) : '—';
}

/** Expanded row content of a risk event: request line, timing and IDs. */
export function RiskEventDetail({ event }: { event: RiskEvent }) {
  const { t } = useTranslation('risk-events');
  const time = (value: RiskEvent['startedAt']) => (
    <span className="tabular text-xs">{formatDateTime(value) || '—'}</span>
  );
  return (
    // Capped so the definition list stays readable in a full-width table row.
    <DetailGrid
      className="max-w-4xl"
      items={[
        {
          key: 'request',
          label: t('detail.request'),
          wide: true,
          value: (
            <span className="font-mono text-xs wrap-anywhere">
              <span className="font-semibold">{event.method || '—'}</span> {event.uri || '—'}
            </span>
          ),
        },
        { key: 'startedAt', label: t('detail.startedAt'), value: time(event.startedAt) },
        { key: 'finishedAt', label: t('detail.finishedAt'), value: time(event.finishedAt) },
        { key: 'processedAt', label: t('detail.processedAt'), value: time(event.createdAt) },
        {
          key: 'duration',
          label: t('detail.duration'),
          value: (
            <span className="tabular text-xs">
              {durationText(event)} ({t('detail.reported', { latency: formatLatency(event.latencyMs) })})
            </span>
          ),
        },
        { key: 'bytes', label: t('detail.bytes'), value: formatBytes(event.responseBytes) },
        { key: 'client', label: t('detail.client'), value: <MonoText value={event.client} /> },
        { key: 'id', label: t('detail.id'), value: <IdText value={event.id} /> },
        { key: 'lease', label: t('columns.lease'), value: <IdText value={event.leaseId} /> },
        { key: 'report', label: t('columns.report'), value: <IdText value={event.reportId} /> },
        { key: 'token', label: t('detail.token'), value: <IdText value={event.tokenId} /> },
        { key: 'group', label: t('detail.groupId'), value: <IdText value={event.endpointGroupId} /> },
      ]}
    />
  );
}
