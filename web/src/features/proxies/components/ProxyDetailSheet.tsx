import { LoaderCircleIcon, StethoscopeIcon } from 'lucide-react';
import { type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';

import { PERMISSIONS } from '@/app/auth/permissions';
import { PermissionButton } from '@/app/auth/PermissionGate';
import { IdText } from '@/components/CopyButton';
import { ErrorState } from '@/components/ErrorState';
import { TimeAgo } from '@/components/TimeAgo';
import { Separator } from '@/components/ui/separator';
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '@/components/ui/sheet';
import { Skeleton } from '@/components/ui/skeleton';
import { type Proxy } from '@/gen/spinneret/v1/proxy_admin_pb';
import { formatLatency, formatNumber } from '@/lib/format';

import { useProxyDetail } from '../useProxies';
import { Countdown } from './Countdown';
import { ProxyActionsMenu, type ProxyAction } from './ProxyActionsMenu';
import { LastCheckCell, ProxyStateCell, TagList } from './ProxyCells';
import { ProxySiteStates } from './ProxySiteStates';

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid min-w-0 gap-0.5">
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="min-w-0 text-sm break-words">{children}</dd>
    </div>
  );
}

const dash = <span className="text-muted-foreground">—</span>;

function ProxyAttributes({ proxy }: { proxy: Proxy }) {
  const { t, i18n } = useTranslation('proxies');
  const lng = i18n.language;
  return (
    <dl className="grid grid-cols-2 gap-x-4 gap-y-3">
      <Field label={t('detail.id')}>
        <IdText value={proxy.id} />
      </Field>
      <Field label={t('detail.endpoint')}>
        <span className="font-mono text-xs">
          {proxy.scheme}://{proxy.host}:{proxy.port}
        </span>
      </Field>
      <Field label={t('detail.username')}>
        {proxy.usernameHint ? <span className="font-mono text-xs">{proxy.usernameHint}</span> : dash}
      </Field>
      <Field label={t('detail.kind')}>{t(`kinds.${proxy.kind}`, { defaultValue: proxy.kind })}</Field>
      <Field label={t('detail.location')}>
        {[proxy.region, proxy.city].filter(Boolean).join(' / ') || dash}
      </Field>
      <Field label={t('detail.provider')}>{proxy.provider || dash}</Field>
      <Field label={t('detail.maxConcurrency')}>{formatNumber(proxy.maxConcurrency, undefined, lng)}</Field>
      <Field label={t('detail.identities')}>{formatNumber(proxy.boundIdentities, undefined, lng)}</Field>
      <Field label={t('detail.tags')}>
        <TagList tags={proxy.tags} />
      </Field>
      <Field label={t('detail.sessionTemplate')}>
        {proxy.sessionTemplate ? <code className="text-xs break-all">{proxy.sessionTemplate}</code> : dash}
      </Field>
      <Field label={t('detail.created')}>
        <TimeAgo value={proxy.createdAt} past />
      </Field>
      <Field label={t('detail.updated')}>
        <TimeAgo value={proxy.updatedAt} past />
      </Field>
    </dl>
  );
}

function ProxyHealth({ proxy }: { proxy: Proxy }) {
  const { t, i18n } = useTranslation('proxies');
  return (
    <dl className="grid grid-cols-2 gap-x-4 gap-y-3">
      <Field label={t('detail.state')}>
        <ProxyStateCell proxy={proxy} />
      </Field>
      <Field label={t('detail.stateReason')}>{proxy.stateReason || dash}</Field>
      <Field label={t('detail.banUntil')}>
        {proxy.state === 'banned' && !proxy.banUntil ? (
          t('state.bannedPermanently')
        ) : (
          <Countdown until={proxy.banUntil} />
        )}
      </Field>
      <Field label={t('detail.cooldownUntil')}>
        <Countdown until={proxy.cooldownUntil} />
      </Field>
      <Field label={t('detail.lastCheck')}>
        <LastCheckCell proxy={proxy} />
      </Field>
      <Field label={t('detail.latency')}>
        {proxy.lastCheckOk ? formatLatency(proxy.lastLatencyMs) : dash}
      </Field>
      <Field label={t('detail.exitIp')}>{proxy.exitIp ? <IdText value={proxy.exitIp} /> : dash}</Field>
      <Field label={t('detail.failures')}>
        {formatNumber(proxy.consecutiveCheckFailures, undefined, i18n.language)}
      </Field>
    </dl>
  );
}

export interface ProxyDetailSheetProps {
  proxyId: string | undefined;
  /** Row data shown while the detail request loads. */
  initial?: Proxy;
  onOpenChange: (open: boolean) => void;
  onAction: (action: ProxyAction, proxy: Proxy) => void;
  checking: boolean;
  /** Pause auto refresh (a dialog is open on top). */
  paused: boolean;
}

/** Side panel with proxy attributes, health and live per-site state. */
export function ProxyDetailSheet({
  proxyId,
  initial,
  onOpenChange,
  onAction,
  checking,
  paused,
}: ProxyDetailSheetProps) {
  const { t } = useTranslation('proxies');
  const query = useProxyDetail(proxyId, paused);
  const proxy = query.data?.proxy ?? initial;

  return (
    <Sheet open={proxyId !== undefined} onOpenChange={onOpenChange}>
      <SheetContent className="w-full sm:max-w-2xl">
        <SheetHeader>
          <SheetTitle className="font-mono text-sm break-all">
            {proxy?.displayUrl ?? t('detail.title')}
          </SheetTitle>
          <SheetDescription>{t('detail.description')}</SheetDescription>
          {proxy && (
            <div className="flex flex-wrap items-center gap-2 pt-2">
              <PermissionButton
                permission={PERMISSIONS.proxyOperate}
                variant="outline"
                size="sm"
                disabled={checking}
                onClick={() => onAction('check', proxy)}
              >
                {checking ? <LoaderCircleIcon className="animate-spin" /> : <StethoscopeIcon />}
                {checking ? t('check.running') : t('check.now')}
              </PermissionButton>
              <PermissionButton
                permission={PERMISSIONS.proxyWrite}
                variant="outline"
                size="sm"
                onClick={() => onAction('edit', proxy)}
              >
                {t('common:actions.edit')}
              </PermissionButton>
              <ProxyActionsMenu proxy={proxy} onAction={onAction} hideDetails triggerVariant="outline" />
            </div>
          )}
        </SheetHeader>
        <div className="grid gap-5 px-4 pb-6">
          {!proxy && query.isLoading && <Skeleton className="h-64 w-full" />}
          {!proxy && query.isError && (
            <ErrorState error={query.error} onRetry={() => void query.refetch()} compact />
          )}
          {proxy && (
            <>
              {query.isError && (
                <ErrorState error={query.error} onRetry={() => void query.refetch()} compact />
              )}
              <section className="grid gap-2">
                <h3 className="text-sm font-semibold">{t('detail.health')}</h3>
                <ProxyHealth proxy={proxy} />
              </section>
              <Separator />
              <section className="grid gap-2">
                <h3 className="text-sm font-semibold">{t('detail.sites')}</h3>
                {query.isLoading ? (
                  <Skeleton className="h-24 w-full" />
                ) : (
                  <ProxySiteStates sites={proxy.sites} />
                )}
              </section>
              <Separator />
              <section className="grid gap-2">
                <h3 className="text-sm font-semibold">{t('detail.attributes')}</h3>
                <ProxyAttributes proxy={proxy} />
              </section>
            </>
          )}
        </div>
      </SheetContent>
    </Sheet>
  );
}
