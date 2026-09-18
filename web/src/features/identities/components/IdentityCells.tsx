import { useTranslation } from 'react-i18next';

import { StateBadge } from '@/components/StateBadge';
import { Badge } from '@/components/ui/badge';
import { SimpleTooltip } from '@/components/ui/tooltip';
import { type Identity } from '@/gen/spinneret/v1/identity_admin_pb';
import { formatDateTime, toDate } from '@/lib/time';

const TAGS_SHOWN = 2;

/** State badge with reason and end time in a tooltip. */
export function StateCell({ identity }: { identity: Identity }) {
  const { t } = useTranslation('identities');
  const until =
    identity.state === 'banned'
      ? identity.banUntil
      : identity.state === 'quarantined'
        ? identity.quarantineUntil
        : undefined;
  const lines: string[] = [];
  if (identity.stateReason) lines.push(t('state.reason', { reason: identity.stateReason }));
  if (toDate(until)) lines.push(t('state.until', { time: formatDateTime(until) }));
  else if (identity.state === 'banned') lines.push(t('state.permanent'));
  return (
    <SimpleTooltip content={lines.length > 0 ? lines.join(' · ') : undefined}>
      <span className="inline-flex">
        <StateBadge kind="identity" state={identity.state} />
      </span>
    </SimpleTooltip>
  );
}

/** First tags as badges with the rest counted. */
export function TagsCell({ tags }: { tags: readonly string[] }) {
  if (tags.length === 0) return <span className="text-muted-foreground">—</span>;
  const shown = tags.slice(0, TAGS_SHOWN);
  const rest = tags.length - shown.length;
  return (
    <SimpleTooltip content={tags.join(', ')}>
      <span className="inline-flex max-w-48 items-center gap-1">
        {shown.map((tag) => (
          <Badge key={tag} variant="secondary" className="max-w-24 truncate font-normal">
            {tag}
          </Badge>
        ))}
        {rest > 0 && <span className="text-xs text-muted-foreground">+{rest}</span>}
      </span>
    </SimpleTooltip>
  );
}

/** Text or a muted dash when empty. */
export function Muted({ value }: { value: string }) {
  return value ? <span>{value}</span> : <span className="text-muted-foreground">—</span>;
}
