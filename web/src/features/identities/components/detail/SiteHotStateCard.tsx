import { Link } from '@tanstack/react-router';
import { DatabaseZapIcon } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { CopyButton } from '@/components/CopyButton';
import { StateBadge } from '@/components/StateBadge';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { type Identity, type IdentityHotState } from '@/gen/spinneret/v1/identity_admin_pb';
import { formatNumber } from '@/lib/format';

import { Countdown } from '../Countdown';
import { ScoreBar } from '../ScoreBar';
import { DetailList, DetailRow } from './DetailList';

export interface SiteHotStateCardProps {
  identity: Identity;
  hot: IdentityHotState | undefined;
}

/** Site-level live state from Redis: scheduler state, leases, cooldowns, exclusivity and bound proxy. */
export function SiteHotStateCard({ identity, hot }: SiteHotStateCardProps) {
  const { t, i18n } = useTranslation('identities');
  const present = hot?.present === true;
  const proxyId = hot?.boundProxyId || identity.boundProxyId;

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('hot.siteTitle')}</CardTitle>
        <CardDescription>{t('hot.siteDescription')}</CardDescription>
      </CardHeader>
      <CardContent>
        {!present && (
          <p className="mb-2 flex items-start gap-2 rounded-md border border-dashed px-3 py-2 text-sm text-muted-foreground">
            <DatabaseZapIcon className="mt-0.5 size-4 shrink-0" aria-hidden />
            {t('hot.notPresent')}
          </p>
        )}
        <DetailList>
          <DetailRow label={t('hot.schedulerState')}>
            {present && hot?.state ? <StateBadge kind="identity" state={hot.state} /> : '—'}
            {present && hot?.state && hot.state !== identity.state && (
              <span className="ml-2 text-xs text-amber-600 dark:text-amber-400">
                {t('hot.stateMismatch')}
              </span>
            )}
          </DetailRow>
          <DetailRow label={t('columns.globalScore')}>
            <ScoreBar
              score={present ? (hot?.globalScore ?? 0) : identity.globalScore}
              samples={present ? hot?.globalSamples : identity.globalSamples}
            />
          </DetailRow>
          <DetailRow label={t('columns.activeLeases')}>
            <span className="tabular">
              {formatNumber(present ? hot?.activeLeases : identity.activeLeases, undefined, i18n.language)}
            </span>
          </DetailRow>
          <DetailRow label={t('hot.siteCooldownUntil')}>
            <Countdown until={hot?.siteCooldownUntil} />
          </DetailRow>
          <DetailRow label={t('hot.siteReuseUntil')}>
            <Countdown until={hot?.siteReuseUntil} />
          </DetailRow>
          <DetailRow label={t('hot.exclusiveUntil')}>
            <Countdown until={hot?.exclusiveUntil} />
          </DetailRow>
          <DetailRow label={t('hot.accountCooldownUntil')}>
            <Countdown until={hot?.accountCooldownUntil} />
          </DetailRow>
          <DetailRow label={t('hot.boundProxy')}>
            {proxyId ? (
              <span className="inline-flex max-w-full items-start gap-0.5">
                <Link
                  to="/proxies"
                  search={{ search: proxyId }}
                  className="min-w-0 font-mono text-xs wrap-anywhere hover:underline"
                >
                  {proxyId}
                </Link>
                <CopyButton value={proxyId} className="-my-1 shrink-0" />
              </span>
            ) : (
              <span className="text-muted-foreground">{t('hot.unbound')}</span>
            )}
          </DetailRow>
        </DetailList>
      </CardContent>
    </Card>
  );
}
