import { CircleCheckIcon, CircleXIcon, SnowflakeIcon } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { StateBadge } from '@/components/StateBadge';
import { TimeAgo } from '@/components/TimeAgo';
import { Badge } from '@/components/ui/badge';
import { SimpleTooltip } from '@/components/ui/tooltip';
import { type Proxy } from '@/gen/spinneret/v1/proxy_admin_pb';
import { useNow } from '@/lib/clock';
import { formatLatency } from '@/lib/format';
import { formatDateTime, toDate } from '@/lib/time';

/** State badge with reason, ban end and global cooldown in the tooltip. */
export function ProxyStateCell({ proxy }: { proxy: Proxy }) {
  const { t } = useTranslation('proxies');
  const now = useNow();
  const cooldownUntil = toDate(proxy.cooldownUntil);
  const cooling = cooldownUntil !== undefined && cooldownUntil.getTime() > now;
  const banUntil = toDate(proxy.banUntil);

  const lines = [
    proxy.stateReason ? t('state.reason', { reason: proxy.stateReason }) : undefined,
    proxy.state === 'banned'
      ? banUntil
        ? t('state.banUntil', { time: formatDateTime(banUntil) })
        : t('state.bannedPermanently')
      : undefined,
    cooling ? t('state.cooldownUntil', { time: formatDateTime(cooldownUntil) }) : undefined,
    proxy.stateChangedAt ? t('state.changed', { time: formatDateTime(proxy.stateChangedAt) }) : undefined,
  ].filter((line): line is string => Boolean(line));

  return (
    <SimpleTooltip
      enabled={lines.length > 0}
      content={
        <div className="grid gap-0.5">
          {lines.map((line) => (
            <span key={line}>{line}</span>
          ))}
        </div>
      }
    >
      <span className="inline-flex items-center gap-1" tabIndex={lines.length > 0 ? 0 : undefined}>
        <StateBadge kind="proxy" state={proxy.state} />
        {cooling && (
          <SnowflakeIcon
            className="size-3.5 text-sky-600 dark:text-sky-400"
            aria-label={t('state.cooling')}
          />
        )}
      </span>
    </SimpleTooltip>
  );
}

/** Result of the last health check: status icon, latency and time. */
export function LastCheckCell({ proxy }: { proxy: Proxy }) {
  const { t } = useTranslation('proxies');
  if (!toDate(proxy.lastCheckAt)) {
    return <span className="text-xs text-muted-foreground">{t('check.never')}</span>;
  }
  return (
    <span className="inline-flex items-center gap-1.5 text-xs">
      {proxy.lastCheckOk ? (
        <CircleCheckIcon
          className="size-3.5 text-emerald-600 dark:text-emerald-400"
          aria-label={t('check.okLabel')}
        />
      ) : (
        <CircleXIcon
          className="size-3.5 text-rose-600 dark:text-rose-400"
          aria-label={t('check.failLabel')}
        />
      )}
      {proxy.lastCheckOk && <span className="tabular">{formatLatency(proxy.lastLatencyMs)}</span>}
      <TimeAgo value={proxy.lastCheckAt} past className="text-muted-foreground" />
    </span>
  );
}

const VISIBLE_TAGS = 2;

/** First tags as badges; the rest in a "+n" tooltip. */
export function TagList({ tags }: { tags: readonly string[] }) {
  if (tags.length === 0) return <span className="text-muted-foreground">—</span>;
  const rest = tags.slice(VISIBLE_TAGS);
  return (
    <span className="inline-flex max-w-56 items-center gap-1">
      {tags.slice(0, VISIBLE_TAGS).map((tag) => (
        <Badge key={tag} variant="muted" className="max-w-24 truncate">
          {tag}
        </Badge>
      ))}
      {rest.length > 0 && (
        <SimpleTooltip content={rest.join(', ')}>
          <Badge variant="outline" tabIndex={0}>
            +{rest.length}
          </Badge>
        </SimpleTooltip>
      )}
    </span>
  );
}
