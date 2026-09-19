import { useMutation } from '@tanstack/react-query';
import {
  ArrowUpCircleIcon,
  CheckCircle2Icon,
  ExternalLinkIcon,
  LoaderCircleIcon,
  TriangleAlertIcon,
} from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { useAuth } from '@/app/auth/AuthContext';
import { TimeAgo } from '@/components/TimeAgo';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { type CheckForUpdateResponse } from '@/gen/spinneret/v1/system_pb';
import { systemClient } from '@/lib/clients';
import { errorMessage } from '@/lib/errors';
import { CHANGELOG_URL } from '@/lib/project';

/**
 * Current build and an on-demand check against the published releases.
 *
 * Nothing here runs on a timer: the deployment reaches out only when an operator
 * presses the button, and an operator who wants no outbound call at all sets
 * SPINNERET_UPDATE_CHECK_URL to the empty string, after which the server answers
 * "disabled" and the button is gone.
 */
export function UpdateCard() {
  const { t } = useTranslation();
  const { serverVersion } = useAuth();

  const check = useMutation<CheckForUpdateResponse, unknown, void>({
    mutationFn: () => systemClient.checkForUpdate({}),
  });
  const result = check.data;
  const disabled = result?.disabled ?? false;

  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between gap-2">
        <CardTitle>{t('system.updates.title')}</CardTitle>
        {!disabled && (
          <Button size="sm" variant="outline" onClick={() => check.mutate()} disabled={check.isPending}>
            {check.isPending && <LoaderCircleIcon className="size-4 animate-spin" aria-hidden />}
            {t('system.updates.check')}
          </Button>
        )}
      </CardHeader>
      <CardContent className="space-y-3">
        <dl className="grid grid-cols-[auto_1fr] items-baseline gap-x-4 gap-y-1.5 text-sm">
          <dt className="text-muted-foreground">{t('system.updates.current')}</dt>
          <dd className="font-mono text-xs">
            {result?.currentVersion || serverVersion || t('system.updates.unknown')}
          </dd>
          {result?.latestVersion && (
            <>
              <dt className="text-muted-foreground">{t('system.updates.latest')}</dt>
              <dd className="font-mono text-xs">{result.latestVersion}</dd>
            </>
          )}
          {result?.checkedAt && (
            <>
              <dt className="text-muted-foreground">{t('system.updates.checkedAt')}</dt>
              <dd>
                <TimeAgo value={result.checkedAt} />
              </dd>
            </>
          )}
        </dl>

        {disabled && <p className="text-sm text-muted-foreground">{t('system.updates.disabled')}</p>}

        {check.isError && (
          <p className="flex items-start gap-2 text-sm text-destructive">
            <TriangleAlertIcon className="mt-0.5 size-4 shrink-0" aria-hidden />
            <span>{errorMessage(check.error, t)}</span>
          </p>
        )}

        {/* The RPC succeeds even when the feed could not be read, so that the
            running build stays visible; the reason travels in the response. */}
        {result?.error && (
          <p className="flex items-start gap-2 text-sm text-muted-foreground">
            <TriangleAlertIcon className="mt-0.5 size-4 shrink-0" aria-hidden />
            <span>{t('system.updates.failed', { error: result.error })}</span>
          </p>
        )}

        {result?.updateAvailable && (
          <div className="flex flex-wrap items-center gap-2 rounded-md border bg-muted/40 px-3 py-2">
            <Badge variant="default" className="gap-1">
              <ArrowUpCircleIcon className="size-3.5" aria-hidden />
              {t('system.updates.available', { version: result.latestVersion })}
            </Badge>
            <a
              href={result.releaseUrl || CHANGELOG_URL}
              target="_blank"
              rel="noreferrer noopener"
              className="inline-flex items-center gap-1 text-sm underline underline-offset-4 hover:text-primary"
            >
              {t('system.updates.releaseNotes')}
              <ExternalLinkIcon className="size-3.5" aria-hidden />
            </a>
          </div>
        )}

        {result && !result.updateAvailable && !result.error && !disabled && (
          <p className="flex items-center gap-2 text-sm text-muted-foreground">
            <CheckCircle2Icon className="size-4 shrink-0" aria-hidden />
            {result.latestVersion ? t('system.updates.upToDate') : t('system.updates.noReleases')}
          </p>
        )}
      </CardContent>
    </Card>
  );
}
