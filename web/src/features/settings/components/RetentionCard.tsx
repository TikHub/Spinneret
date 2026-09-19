import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { LockIcon, LoaderCircleIcon, RotateCcwIcon, TriangleAlertIcon } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';

import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip';
import { SettingOrigin, type Setting } from '@/gen/spinneret/v1/system_pb';
import { systemClient } from '@/lib/clients';
import { errorMessage } from '@/lib/errors';

/** The settings this card edits, in the order an operator reaches for them. */
const ORDER = [
  'retention.risk_events',
  'retention.minute_stats',
  'retention.hour_stats',
  'retention.state_events',
  'retention.audit',
  'retention.alert_events',
  'retention.clickhouse_ttl_days',
] as const;

function sortSettings(settings: Setting[]): Setting[] {
  const rank = (key: string) => {
    const i = ORDER.indexOf(key as (typeof ORDER)[number]);
    return i === -1 ? ORDER.length : i;
  };
  return [...settings].sort((a, b) => rank(a.key) - rank(b.key));
}

/**
 * Deployment retention, editable without a restart.
 *
 * A setting an environment variable pins is shown with its value and a lock: a
 * deployment managed from a file keeps the guarantee that the file is what runs,
 * and the operator is told which variable to remove to take it over here.
 */
export function RetentionCard({ canEdit }: { canEdit: boolean }) {
  const { t } = useTranslation();
  const qc = useQueryClient();

  const settings = useQuery({
    queryKey: ['system', 'settings'],
    queryFn: () => systemClient.listSettings({}),
  });

  // Edits live here until saved, so Save can send only what actually changed.
  // Nothing clears this on a refetch on purpose: a value arriving from the server
  // while someone is typing must not overwrite what they typed. A successful save
  // clears it below, which is the only moment the draft is stale.
  const [draft, setDraft] = useState<Record<string, string>>({});

  const save = useMutation({
    mutationFn: (values: Record<string, string>) => systemClient.updateSettings({ values }),
    onSuccess: async () => {
      setDraft({});
      await qc.invalidateQueries({ queryKey: ['system', 'settings'] });
    },
  });

  const list = sortSettings(settings.data?.settings ?? []);
  const editable = canEdit && (settings.data?.canEdit ?? false);
  const changed = Object.keys(draft).length > 0;

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('system.retention.title')}</CardTitle>
        <CardDescription>{t('system.retention.description')}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        {settings.isPending && (
          <p className="flex items-center gap-2 text-sm text-muted-foreground">
            <LoaderCircleIcon className="size-4 animate-spin" aria-hidden />
            {t('table.loading')}
          </p>
        )}

        {settings.isError && (
          <p className="flex items-start gap-2 text-sm text-destructive">
            <TriangleAlertIcon className="mt-0.5 size-4 shrink-0" aria-hidden />
            <span>{errorMessage(settings.error, t)}</span>
          </p>
        )}

        {list.length > 0 && (
          <div className="grid gap-3">
            {list.map((s) => {
              const pinned = s.origin === SettingOrigin.ENVIRONMENT;
              const current = draft[s.key] ?? s.value;
              const isDefault = s.origin === SettingOrigin.DEFAULT;
              return (
                <div key={s.key} className="grid gap-1.5 sm:grid-cols-[1fr_auto] sm:items-center sm:gap-4">
                  <div className="min-w-0">
                    <label htmlFor={s.key} className="block text-sm">
                      {t(`system.retention.keys.${s.key}` as const, { defaultValue: s.key })}
                    </label>
                    <p className="text-xs text-muted-foreground">
                      {t(`system.retention.hints.${s.key}` as const, { defaultValue: '' })}
                    </p>
                  </div>
                  <div className="flex items-center gap-2">
                    {pinned && (
                      <Tooltip>
                        <TooltipTrigger asChild>
                          <Badge variant="outline" className="gap-1">
                            <LockIcon className="size-3" aria-hidden />
                            {t('system.retention.pinned')}
                          </Badge>
                        </TooltipTrigger>
                        <TooltipContent>
                          {t('system.retention.pinnedBy', { envVar: s.envVar })}
                        </TooltipContent>
                      </Tooltip>
                    )}
                    {!pinned && !isDefault && (
                      <Badge variant="secondary">{t('system.retention.customized')}</Badge>
                    )}
                    <Input
                      id={s.key}
                      value={current}
                      disabled={pinned || !editable || save.isPending}
                      inputMode={s.unit === 'days' ? 'numeric' : 'text'}
                      aria-describedby={`${s.key}-range`}
                      className="w-32 font-mono text-xs"
                      onChange={(e) => setDraft((d) => ({ ...d, [s.key]: e.target.value }))}
                    />
                    <span id={`${s.key}-range`} className="w-24 shrink-0 text-xs text-muted-foreground">
                      {s.unit === 'days' ? t('system.retention.days') : ''} {s.minimum}–{s.maximum}
                    </span>
                    {editable && !pinned && !isDefault && (
                      <Tooltip>
                        <TooltipTrigger asChild>
                          <Button
                            size="icon"
                            variant="ghost"
                            aria-label={t('system.retention.reset', { value: s.defaultValue })}
                            disabled={save.isPending}
                            onClick={() => save.mutate({ [s.key]: '' })}
                          >
                            <RotateCcwIcon className="size-4" aria-hidden />
                          </Button>
                        </TooltipTrigger>
                        <TooltipContent>
                          {t('system.retention.reset', { value: s.defaultValue })}
                        </TooltipContent>
                      </Tooltip>
                    )}
                  </div>
                </div>
              );
            })}
          </div>
        )}

        {save.isError && (
          <p className="flex items-start gap-2 text-sm text-destructive">
            <TriangleAlertIcon className="mt-0.5 size-4 shrink-0" aria-hidden />
            <span>{errorMessage(save.error, t)}</span>
          </p>
        )}

        {!editable && list.length > 0 && (
          <p className="text-sm text-muted-foreground">{t('system.retention.readOnly')}</p>
        )}

        {editable && (
          <div className="flex items-center gap-3">
            <Button onClick={() => save.mutate(draft)} disabled={!changed || save.isPending}>
              {save.isPending && <LoaderCircleIcon className="size-4 animate-spin" aria-hidden />}
              {t('actions.save')}
            </Button>
            {changed && (
              <Button variant="ghost" onClick={() => setDraft({})} disabled={save.isPending}>
                {t('actions.cancel')}
              </Button>
            )}
            {/* Shortening a retention is not a preference; the next hourly pass
                drops the partitions it now covers, and dropped is dropped. */}
            <p className="text-xs text-muted-foreground">{t('system.retention.warning')}</p>
          </div>
        )}
      </CardContent>
    </Card>
  );
}
