import { useQueryClient } from '@tanstack/react-query';
import { CircleAlertIcon, CircleCheckIcon, KeyRoundIcon, RefreshCwIcon } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import { PERMISSIONS } from '@/app/auth/permissions';
import { PermissionButton } from '@/app/auth/PermissionGate';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { ErrorState } from '@/components/ErrorState';
import { StatCard } from '@/components/StatCard';
import { TimeAgo } from '@/components/TimeAgo';
import { Badge } from '@/components/ui/badge';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import {
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table';
import { type GetKEKStatusResponse } from '@/gen/spinneret/v1/secret_admin_pb';
import { secretAdminClient } from '@/lib/clients';
import { formatNumber, formatPercent } from '@/lib/format';

import { kekProgress, missingKeks, pendingRewrapRecords } from '../kek';
import { useKekStatus } from '../useSecretsApi';

function RewrapProgress({ status }: { status: GetKEKStatusResponse }) {
  const { t, i18n } = useTranslation('secrets');
  const { done, total, ratio } = kekProgress(status);
  return (
    <div className="grid gap-2">
      <div className="flex flex-wrap items-center justify-between gap-2 text-sm">
        <span className="font-medium">
          {status.rewrapRunning ? t('kek.running') : total > 0 ? t('kek.lastJob') : t('kek.noJob')}
        </span>
        {total > 0 && (
          <span className="text-xs text-muted-foreground tabular">
            {t('kek.progress', {
              done: formatNumber(done, undefined, i18n.language),
              total: formatNumber(total, undefined, i18n.language),
              percent: formatPercent(ratio, 1, i18n.language),
            })}
          </span>
        )}
      </div>
      {(status.rewrapRunning || total > 0) && (
        <div
          className="h-2 overflow-hidden rounded-full bg-muted"
          role="progressbar"
          aria-label={t('kek.progressLabel')}
          aria-valuemin={0}
          aria-valuemax={total}
          aria-valuenow={done}
        >
          <div
            className={
              status.lastRewrapError ? 'h-full bg-destructive' : 'h-full bg-primary transition-[width]'
            }
            style={{ width: `${ratio * 100}%` }}
          />
        </div>
      )}
      {!status.rewrapRunning && status.lastRewrapFinishedAt && (
        <p className="text-xs text-muted-foreground">
          {t('kek.finished')} <TimeAgo value={status.lastRewrapFinishedAt} past />
        </p>
      )}
      {status.lastRewrapError && (
        <p role="alert" className="flex items-start gap-1.5 text-xs break-words text-destructive">
          <CircleAlertIcon className="mt-0.5 size-3.5 shrink-0" aria-hidden />
          {t('kek.lastError', { error: status.lastRewrapError })}
        </p>
      )}
    </div>
  );
}

/** KEK status and rewrap control (platform administrators). */
export function KekPanel() {
  const { t, i18n } = useTranslation('secrets');
  const queryClient = useQueryClient();
  const status = useKekStatus(true);
  const [confirmOpen, setConfirmOpen] = useState(false);
  const data = status.data;

  const start = async () => {
    const res = await secretAdminClient.startKEKRewrap({});
    if (res.started) toast.success(t('kek.started'));
    else toast.info(t('kek.alreadyRunning'));
    await queryClient.invalidateQueries({ queryKey: ['secrets'] });
  };

  if (status.isError && !data) {
    return <ErrorState error={status.error} onRetry={() => void status.refetch()} />;
  }
  if (!data) {
    return (
      <div className="grid gap-3" role="status">
        <Skeleton className="h-24 w-full" />
        <Skeleton className="h-48 w-full" />
      </div>
    );
  }

  const pending = pendingRewrapRecords(data);
  const missing = missingKeks(data);

  return (
    <div className="grid gap-4">
      <div className="grid gap-3 sm:grid-cols-3">
        <StatCard
          label={t('kek.current')}
          icon={KeyRoundIcon}
          value={<span className="font-mono">{data.currentKekId || '—'}</span>}
        />
        <StatCard
          label={t('kek.pending')}
          tone={pending > 0 ? 'warning' : 'success'}
          value={formatNumber(pending, undefined, i18n.language)}
          hint={pending > 0 ? t('kek.pendingHint') : t('kek.allCurrent')}
        />
        <StatCard
          label={t('kek.missing')}
          tone={missing.length > 0 ? 'danger' : 'default'}
          value={formatNumber(missing.length, undefined, i18n.language)}
          hint={missing.length > 0 ? t('kek.missingHint') : undefined}
        />
      </div>
      <Card>
        <CardHeader className="flex-row flex-wrap items-start justify-between gap-2">
          <div className="grid gap-1">
            <CardTitle>{t('kek.rewrapTitle')}</CardTitle>
            <CardDescription>{t('kek.rewrapDescription')}</CardDescription>
          </div>
          <PermissionButton
            permission={PERMISSIONS.kekManage}
            size="sm"
            disabled={data.rewrapRunning}
            onClick={() => setConfirmOpen(true)}
          >
            <RefreshCwIcon className={data.rewrapRunning ? 'animate-spin' : undefined} />
            {data.rewrapRunning ? t('kek.running') : t('kek.start')}
          </PermissionButton>
        </CardHeader>
        <CardContent>
          <RewrapProgress status={data} />
        </CardContent>
      </Card>
      <Card>
        <CardHeader>
          <CardTitle>{t('kek.keysTitle')}</CardTitle>
        </CardHeader>
        <CardContent className="px-0">
          <TableContainer>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t('kek.id')}</TableHead>
                  <TableHead>{t('kek.state')}</TableHead>
                  <TableHead>{t('kek.configured')}</TableHead>
                  <TableHead className="text-right">{t('kek.wrappedRecords')}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {data.keks.map((kek) => (
                  <TableRow key={kek.id}>
                    <TableCell className="font-mono text-xs">{kek.id}</TableCell>
                    <TableCell>
                      {kek.current ? (
                        <Badge>{t('kek.currentBadge')}</Badge>
                      ) : (
                        <Badge variant="muted">{t('kek.previous')}</Badge>
                      )}
                    </TableCell>
                    <TableCell>
                      {kek.configured ? (
                        <span className="inline-flex items-center gap-1 text-emerald-600 dark:text-emerald-400">
                          <CircleCheckIcon className="size-3.5" aria-hidden />
                          {t('kek.yes')}
                        </span>
                      ) : (
                        <span className="inline-flex items-center gap-1 text-destructive">
                          <CircleAlertIcon className="size-3.5" aria-hidden />
                          {t('kek.notConfigured')}
                        </span>
                      )}
                    </TableCell>
                    <TableCell className="text-right tabular">
                      {formatNumber(kek.wrappedRecords, undefined, i18n.language)}
                    </TableCell>
                  </TableRow>
                ))}
                {data.keks.length === 0 && (
                  <TableRow>
                    <TableCell colSpan={4} className="py-6 text-center text-muted-foreground">
                      {t('kek.noKeys')}
                    </TableCell>
                  </TableRow>
                )}
              </TableBody>
            </Table>
          </TableContainer>
        </CardContent>
      </Card>
      <ConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title={t('kek.confirmTitle')}
        description={t('kek.confirmDescription', { current: data.currentKekId, count: pending })}
        confirmLabel={t('kek.start')}
        onConfirm={start}
      />
    </div>
  );
}
