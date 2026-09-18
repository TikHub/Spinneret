import { useCallback, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import { type CheckProxyResponse, type Proxy } from '@/gen/spinneret/v1/proxy_admin_pb';
import { errorMessage, type Translate } from '@/lib/errors';
import { formatLatency } from '@/lib/format';

import { useCheckProxy } from './useProxies';

/** Toast description of a check result: latency, exit IP, region or the failure. */
export function describeCheck(res: CheckProxyResponse, t: Translate): string {
  if (!res.ok) return res.error || t('check.noError');
  return [
    t('check.latency', { latency: formatLatency(res.latencyMs) }),
    res.exitIp ? t('check.exitIp', { ip: res.exitIp }) : undefined,
    res.region ? t('check.region', { region: res.region }) : undefined,
  ]
    .filter(Boolean)
    .join(' · ');
}

/** Runs "check now" for proxies, tracking in-flight checks and reporting the result as a toast. */
export function useCheckAction() {
  const { t } = useTranslation('proxies');
  const mutation = useCheckProxy();
  const [checkingIds, setCheckingIds] = useState<ReadonlySet<string>>(() => new Set());
  const { mutateAsync } = mutation;

  const check = useCallback(
    async (proxy: Proxy) => {
      const name = proxy.displayUrl || proxy.id;
      setCheckingIds((prev) => new Set(prev).add(proxy.id));
      try {
        const res = await mutateAsync(proxy.id);
        const description = describeCheck(res, t);
        if (res.ok) toast.success(t('check.ok', { name }), { description });
        else toast.error(t('check.failed', { name }), { description });
      } catch (err) {
        toast.error(t('check.requestFailed', { name }), { description: errorMessage(err, t) });
      } finally {
        setCheckingIds((prev) => {
          const next = new Set(prev);
          next.delete(proxy.id);
          return next;
        });
      }
    },
    [mutateAsync, t],
  );

  return { checkingIds, check };
}
