import { InfoIcon, LoaderCircleIcon, PlusIcon, RotateCcwIcon } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import { PERMISSIONS } from '@/app/auth/permissions';
import { PermissionButton } from '@/app/auth/PermissionGate';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { EmptyState } from '@/components/EmptyState';
import { ErrorState } from '@/components/ErrorState';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet';
import { Skeleton } from '@/components/ui/skeleton';
import { errorMessage, toApiError } from '@/lib/errors';

import { useRuleDraft } from '../useRuleDraft';
import { useSitePermissions } from '../useSitePermissions';
import { useReplaceURIRules, useURIRules } from '../useSites';
import {
  DEFAULT_GROUP_NAME,
  hasBlockingErrors,
  MAX_RULES,
  parseServerRuleIndex,
  URI_RULE_KINDS,
} from '../uriRules';
import { URIRuleRow } from './URIRuleRow';

/** Endpoint group addressed by the editor. */
export interface RuleEditorTarget {
  id: string;
  name: string;
  client: string;
  site: string;
  siteId: string;
}

export interface URIRulesSheetProps {
  target: RuleEditorTarget;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

interface ServerError {
  /** Key of the row the server rejected (from "rules[i]: ..."); undefined for general errors. */
  key?: string;
  message: string;
}

/** Ordered URI rule editor of one endpoint group (ReplaceURIRules). */
export function URIRulesSheet({ target, open, onOpenChange }: URIRulesSheetProps) {
  const { t } = useTranslation('sites');
  const { canWriteSite } = useSitePermissions();
  const isDefault = target.name === DEFAULT_GROUP_NAME;
  const query = useURIRules(isDefault ? undefined : target.id);
  // The draft adopts the first rules it receives and ignores later refetches, so it only accepts
  // rules loaded after this editor opened: a cached list may predate someone else's changes.
  const draft = useRuleDraft(query.isFetchedAfterMount && query.isSuccess ? query.data : undefined);
  const mutation = useReplaceURIRules();
  const [serverError, setServerError] = useState<ServerError>();
  const [confirmDiscard, setConfirmDiscard] = useState(false);
  const readOnly = !canWriteSite(target.siteId) || mutation.isPending;
  const blocking = hasBlockingErrors(draft.hints);

  const requestClose = (next: boolean) => {
    if (next) return;
    if (draft.dirty && !mutation.isPending) setConfirmDiscard(true);
    else if (!mutation.isPending) onOpenChange(false);
  };

  const save = () => {
    setServerError(undefined);
    const rows = draft.rows;
    mutation.mutate(
      { groupId: target.id, rules: rows },
      {
        onSuccess: (res) => {
          draft.actions.saved(res.rules);
          toast.success(t('rules.saved', { count: res.rules.length, name: target.name }));
        },
        onError: (err) => {
          const message = toApiError(err).message;
          // Pin the error to the rejected row (not its index) so it follows the row when the
          // list is reordered afterwards.
          const index = parseServerRuleIndex(message);
          const key = index === undefined ? undefined : rows[index]?.key;
          setServerError({ key, message });
          toast.error(errorMessage(err, t));
        },
      },
    );
  };

  /** Drops a row's server error once the row is edited or removed. */
  const clearRowError = (key: string) =>
    setServerError((current) => (current?.key === key ? undefined : current));

  const reload = async () => {
    setServerError(undefined);
    const res = await query.refetch();
    // A failed refetch keeps the previous data: report the failure instead of adopting it.
    if (res.isError) toast.error(errorMessage(res.error, t));
    else if (res.data) draft.actions.saved(res.data);
  };

  const kindCounts = URI_RULE_KINDS.map(
    (kind) => [kind, draft.rows.filter((r) => r.kind === kind).length] as const,
  ).filter(([, n]) => n > 0);
  const generalError = serverError && serverError.key === undefined ? serverError.message : undefined;

  return (
    <>
      <Sheet open={open} onOpenChange={requestClose}>
        <SheetContent className="w-full sm:max-w-3xl">
          <SheetHeader>
            <SheetTitle>{t('rules.title', { name: target.name })}</SheetTitle>
            <SheetDescription className="font-mono text-xs">
              {target.site} / {target.client} / {target.name}
            </SheetDescription>
          </SheetHeader>
          <div className="grid content-start gap-3 px-4">
            {isDefault ? (
              <EmptyState
                icon={InfoIcon}
                title={t('rules.defaultTitle')}
                description={t('rules.defaultDescription')}
              />
            ) : !draft.ready && query.isError ? (
              <ErrorState error={query.error} onRetry={() => void query.refetch()} compact />
            ) : !draft.ready ? (
              <div className="grid gap-2">
                {Array.from({ length: 4 }, (_, i) => (
                  <Skeleton key={i} className="h-11 w-full" />
                ))}
              </div>
            ) : (
              <>
                <p className="flex gap-2 rounded-md bg-muted/50 px-3 py-2 text-xs text-muted-foreground">
                  <InfoIcon className="mt-0.5 size-3.5 shrink-0" aria-hidden />
                  {t('rules.priority')}
                </p>
                <div className="flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
                  <span>{t('rules.count', { count: draft.rows.length })}</span>
                  {kindCounts.map(([kind, n]) => (
                    <Badge key={kind} variant="muted">
                      {t(`rules.kinds.${kind}`)} {n}
                    </Badge>
                  ))}
                  {draft.dirty && (
                    <Badge
                      variant="outline"
                      className="border-amber-500/40 text-amber-700 dark:text-amber-400"
                    >
                      {t('rules.unsaved')}
                    </Badge>
                  )}
                </div>
                {draft.rows.length === 0 ? (
                  <EmptyState compact title={t('rules.empty')} description={t('rules.emptyDescription')} />
                ) : (
                  <ol className="grid gap-1.5" aria-label={t('rules.listLabel')}>
                    {draft.rows.map((rule, index) => (
                      <URIRuleRow
                        key={rule.key}
                        rule={rule}
                        index={index}
                        count={draft.rows.length}
                        hints={draft.hints.get(rule.key) ?? []}
                        serverError={serverError?.key === rule.key ? serverError.message : undefined}
                        readOnly={readOnly}
                        dragging={draft.drag.dragKey === rule.key}
                        dropTarget={draft.drag.overKey === rule.key && draft.drag.dragKey !== rule.key}
                        armed={draft.drag.armedKey === rule.key}
                        onArm={(armed) => draft.drag.arm(rule.key, armed)}
                        onChange={(patch) => {
                          draft.actions.change(rule.key, patch);
                          clearRowError(rule.key);
                        }}
                        onMove={(to) => draft.actions.move(rule.key, to)}
                        onRemove={() => {
                          draft.actions.remove(rule.key);
                          clearRowError(rule.key);
                        }}
                        onDragStart={(e) => draft.drag.start(rule.key, e)}
                        onDragOver={(e) => draft.drag.over(rule.key, e)}
                        onDrop={(e) => draft.drag.drop(rule.key, e)}
                        onDragEnd={draft.drag.end}
                      />
                    ))}
                  </ol>
                )}
                {generalError && (
                  <p role="alert" className="text-sm text-destructive">
                    {generalError}
                  </p>
                )}
                <div>
                  <PermissionButton
                    permission={PERMISSIONS.siteWrite}
                    site={target.siteId}
                    variant="outline"
                    size="sm"
                    onClick={draft.actions.add}
                    disabled={mutation.isPending || draft.rows.length >= MAX_RULES}
                  >
                    <PlusIcon />
                    {t('rules.add')}
                  </PermissionButton>
                </div>
              </>
            )}
          </div>
          <SheetFooter className="flex-row flex-wrap justify-end border-t">
            {!isDefault && (
              <Button
                variant="ghost"
                className="mr-auto"
                onClick={() => (draft.dirty ? draft.actions.reset() : void reload())}
                disabled={mutation.isPending || query.isFetching}
              >
                <RotateCcwIcon />
                {draft.dirty ? t('rules.discard') : t('rules.reload')}
              </Button>
            )}
            <Button variant="outline" onClick={() => requestClose(false)} disabled={mutation.isPending}>
              {t('common:actions.close')}
            </Button>
            {!isDefault && (
              <PermissionButton
                permission={PERMISSIONS.siteWrite}
                site={target.siteId}
                onClick={save}
                disabled={!draft.ready || !draft.dirty || blocking || mutation.isPending}
              >
                {mutation.isPending && <LoaderCircleIcon className="animate-spin" />}
                {t('common:actions.save')}
              </PermissionButton>
            )}
          </SheetFooter>
        </SheetContent>
      </Sheet>
      <ConfirmDialog
        open={confirmDiscard}
        onOpenChange={setConfirmDiscard}
        destructive
        title={t('rules.discardTitle')}
        description={t('rules.discardDescription')}
        confirmLabel={t('rules.discard')}
        onConfirm={() => {
          draft.actions.reset();
          onOpenChange(false);
        }}
      />
    </>
  );
}
