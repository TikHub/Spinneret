import { useCallback, useRef } from 'react';
import { useTranslation } from 'react-i18next';

import { IdText } from '@/components/CopyButton';
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '@/components/ui/sheet';
import { type RequestEvent } from '@/gen/spinneret/v1/dashboard_pb';
import { formatBytes, formatLatency } from '@/lib/format';
import { formatDateTime } from '@/lib/time';

import { DetailGrid, type DetailItem } from '../shared/DetailGrid';
import { HttpStatus, IdentityLink, MarkerList, MonoText, OutcomeBadge } from '../shared/EventCells';

export interface RequestEventSheetProps {
  event: RequestEvent | undefined;
  onOpenChange: (open: boolean) => void;
}

function eventItems(event: RequestEvent, t: (key: string) => string): DetailItem[] {
  const time = (value: RequestEvent['eventTime']) => (
    <span className="tabular">{formatDateTime(value) || '—'}</span>
  );
  return [
    {
      key: 'request',
      label: t('columns.request'),
      wide: true,
      value: (
        <span className="font-mono text-xs wrap-anywhere">
          <span className="font-semibold">{event.method || '—'}</span> {event.uri || '—'}
        </span>
      ),
    },
    { key: 'outcome', label: t('columns.outcome'), value: <OutcomeBadge outcome={event.outcome} /> },
    { key: 'outcomeHint', label: t('columns.outcomeHint'), value: <MonoText value={event.outcomeHint} /> },
    { key: 'blame', label: t('columns.blame'), value: <MonoText value={event.blame} /> },
    { key: 'rule', label: t('columns.rule'), value: <MonoText value={event.rule} /> },
    { key: 'httpStatus', label: t('columns.httpStatus'), value: <HttpStatus status={event.httpStatus} /> },
    { key: 'businessCode', label: t('columns.businessCode'), value: <MonoText value={event.businessCode} /> },
    { key: 'errorKind', label: t('columns.errorKind'), value: <MonoText value={event.errorKind} /> },
    { key: 'markers', label: t('columns.markers'), value: <MarkerList markers={event.markers} max={10} /> },
    { key: 'latency', label: t('columns.latency'), value: formatLatency(event.latencyMs) },
    { key: 'bytes', label: t('columns.bytes'), value: formatBytes(event.responseBytes) },
    { key: 'site', label: t('detail.site'), value: event.site || '—' },
    {
      key: 'group',
      label: t('detail.endpointGroup'),
      value: event.endpointGroup ? `${event.client}/${event.endpointGroup}` : t('shared.unknownGroup'),
    },
    {
      key: 'identity',
      label: t('columns.identity'),
      value: <IdentityLink id={event.identityId} site={event.site} />,
    },
    { key: 'identityType', label: t('columns.identityType'), value: <MonoText value={event.identityType} /> },
    { key: 'proxy', label: t('columns.proxy'), value: <IdText value={event.proxyId} /> },
    { key: 'node', label: t('columns.node'), value: <MonoText value={event.node} /> },
    { key: 'token', label: t('columns.token'), value: <IdText value={event.tokenId} /> },
    { key: 'lease', label: t('columns.lease'), value: <IdText value={event.leaseId} /> },
    { key: 'report', label: t('columns.report'), value: <IdText value={event.reportId} /> },
    { key: 'startedAt', label: t('detail.startedAt'), value: time(event.startedAt) },
    { key: 'eventTime', label: t('detail.finishedAt'), value: time(event.eventTime) },
    { key: 'receivedAt', label: t('columns.receivedAt'), value: time(event.receivedAt) },
    {
      key: 'flags',
      label: t('columns.flags'),
      wide: true,
      value: (
        <span className="text-xs text-muted-foreground">
          {[
            `${t('flags.suppressed')}: ${event.suppressed ? t('detail.yes') : t('detail.no')}`,
            `${t('flags.late')}: ${event.late ? t('detail.yes') : t('detail.no')}`,
            `${t('flags.probe')}: ${event.probe ? t('detail.yes') : t('detail.no')}`,
          ].join(' · ')}
        </span>
      ),
    },
  ];
}

/** Side sheet with every field of one request event. */
export function RequestEventSheet({ event, onOpenChange }: RequestEventSheetProps) {
  const { t } = useTranslation('requests');
  const panelRef = useRef<HTMLDivElement>(null);

  // On open the dialog focuses its first focusable child, which here is a copy
  // button: its tooltip then pops open over the neighbouring fields and eats the
  // first Escape press. The panel itself carries tabindex="-1", so focusing it
  // keeps the focus trap intact without opening anything.
  const focusPanel = useCallback((event: Event) => {
    event.preventDefault();
    panelRef.current?.focus();
  }, []);

  return (
    <Sheet open={event !== undefined} onOpenChange={onOpenChange}>
      <SheetContent
        ref={panelRef}
        side="right"
        className="w-full gap-0 overflow-x-hidden overflow-y-hidden sm:max-w-2xl"
        closeLabel={t('common:actions.close')}
        onOpenAutoFocus={focusPanel}
      >
        {event && (
          <>
            {/*
             * The fields scroll in their own box rather than in the panel, so
             * the header and the panel's absolutely positioned close button
             * both stay in place. A sticky header inside the panel would take
             * the close button's place in the stacking order and swallow its
             * clicks.
             */}
            <SheetHeader className="border-b">
              <SheetTitle>{t('detail.title')}</SheetTitle>
              <SheetDescription className="tabular">{formatDateTime(event.eventTime)}</SheetDescription>
            </SheetHeader>
            <div className="min-h-0 flex-1 overflow-x-hidden overflow-y-auto">
              <DetailGrid className="px-4 pb-6" items={eventItems(event, t)} />
            </div>
          </>
        )}
      </SheetContent>
    </Sheet>
  );
}
