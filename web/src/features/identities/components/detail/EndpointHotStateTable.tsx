import { CheckIcon, MinusIcon } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { CopyButton } from '@/components/CopyButton';
import { EmptyState } from '@/components/EmptyState';
import { TimeAgo } from '@/components/TimeAgo';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { type EndpointHotState } from '@/gen/spinneret/v1/identity_admin_pb';
import { formatNumber } from '@/lib/format';
import { cn } from '@/lib/utils';

import { endpointAvailability, sortEndpointGroups, type EndpointAvailability } from '../../hotState';
import { useSecondTicker } from '../../useTicker';
import { Countdown } from '../Countdown';
import { ScoreBar } from '../ScoreBar';

const AVAILABILITY_CLASS: Record<EndpointAvailability, string> = {
  ready: 'border-emerald-500/25 bg-emerald-500/10 text-emerald-700 dark:text-emerald-400',
  queued: 'border-sky-500/25 bg-sky-500/10 text-sky-700 dark:text-sky-400',
  cooldown: 'border-rose-500/25 bg-rose-500/10 text-rose-700 dark:text-rose-400',
  reuse: 'border-amber-500/25 bg-amber-500/10 text-amber-700 dark:text-amber-400',
  waiting: 'border-zinc-500/25 bg-zinc-500/10 text-zinc-600 dark:text-zinc-400',
};

function AvailabilityBadge({ group }: { group: EndpointHotState }) {
  const { t } = useTranslation('identities');
  const now = useSecondTicker();
  const status = endpointAvailability(group, now);
  return (
    <span
      className={cn(
        'inline-flex rounded-full border px-2 py-0.5 text-xs font-medium',
        AVAILABILITY_CLASS[status],
      )}
    >
      {t(`hot.availability.${status}`)}
    </span>
  );
}

export interface EndpointHotStateTableProps {
  groups: readonly EndpointHotState[];
}

/** Per endpoint group live state: score, samples, failure streak, cooldown and reuse countdowns, queue. */
export function EndpointHotStateTable({ groups }: EndpointHotStateTableProps) {
  const { t, i18n } = useTranslation('identities');
  const sorted = sortEndpointGroups(groups);
  const head = [
    t('hot.group'),
    t('hot.status'),
    t('hot.score'),
    t('hot.samples'),
    t('hot.failures'),
    t('hot.cooldownUntil'),
    t('hot.reuseUntil'),
    t('columns.lastUsed'),
    t('hot.availableAt'),
    t('hot.inReadyQueue'),
  ];

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('hot.groupsTitle')}</CardTitle>
        <CardDescription>{t('hot.groupsDescription')}</CardDescription>
      </CardHeader>
      <CardContent>
        {sorted.length === 0 ? (
          <EmptyState compact title={t('hot.noGroups')} description={t('hot.noGroupsDescription')} />
        ) : (
          <div className="overflow-x-auto rounded-md border">
            <table className="w-full text-sm">
              <thead className="bg-muted/60">
                <tr>
                  {head.map((label) => (
                    <th
                      key={label}
                      scope="col"
                      className="h-8 px-3 text-left text-xs font-medium whitespace-nowrap text-muted-foreground"
                    >
                      {label}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {sorted.map((group) => (
                  <tr key={group.endpointGroupId || group.endpointGroup} className="border-t">
                    <td className="px-3 py-2 whitespace-nowrap">
                      <span className="text-muted-foreground">{group.client} / </span>
                      <span className="font-mono text-xs">{group.endpointGroup}</span>
                      {group.endpointGroupId && (
                        <CopyButton value={group.endpointGroupId} label={group.endpointGroupId} />
                      )}
                    </td>
                    <td className="px-3 py-2">
                      <AvailabilityBadge group={group} />
                    </td>
                    <td className="px-3 py-2">
                      <ScoreBar score={group.score} samples={group.samples} />
                    </td>
                    <td className="tabular px-3 py-2 text-right">
                      {formatNumber(group.samples, undefined, i18n.language)}
                    </td>
                    <td
                      className={cn(
                        'tabular px-3 py-2 text-right',
                        group.consecutiveFailures > 0 && 'font-medium text-rose-600 dark:text-rose-400',
                      )}
                    >
                      {formatNumber(group.consecutiveFailures, undefined, i18n.language)}
                    </td>
                    <td className="px-3 py-2">
                      <Countdown until={group.cooldownUntil} />
                    </td>
                    <td className="px-3 py-2">
                      <Countdown until={group.reuseUntil} />
                    </td>
                    <td className="px-3 py-2 whitespace-nowrap">
                      <TimeAgo value={group.lastUsedAt} fallback={t('common:time.never')} past />
                    </td>
                    <td className="px-3 py-2">
                      <Countdown until={group.availableAt} fallback={t('hot.now')} ended={t('hot.now')} />
                    </td>
                    <td className="px-3 py-2">
                      {group.inReadyQueue ? (
                        <CheckIcon className="size-4 text-emerald-500" aria-label={t('hot.yes')} />
                      ) : (
                        <MinusIcon className="size-4 text-muted-foreground" aria-label={t('hot.no')} />
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </CardContent>
    </Card>
  );
}
