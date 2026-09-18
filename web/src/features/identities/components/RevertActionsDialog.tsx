import { useMutation } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { FlaskConicalIcon, LoaderCircleIcon, Undo2Icon } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import { useAuth } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';
import { PermissionButton } from '@/app/auth/PermissionGate';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { CopyButton } from '@/components/CopyButton';
import { Button } from '@/components/ui/button';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { type RevertActionsResponse } from '@/gen/spinneret/v1/identity_admin_pb';
import { identityClient } from '@/lib/clients';
import { errorMessage } from '@/lib/errors';

import { hasFormErrors } from '../operations';
import { notifyBulkResult, useInvalidateIdentityData } from '../notify';
import { buildRevertRequest, defaultRevertForm, validateRevertForm, type RevertForm } from '../revert';
import { BulkResultView } from './BulkResultDialog';
import { RevertActionsFields } from './RevertActionsFields';

function AffectedIds({ ids }: { ids: readonly string[] }) {
  const { t } = useTranslation('identities');
  if (ids.length === 0) return null;
  return (
    <div className="grid gap-1.5">
      <div className="flex items-center justify-between">
        <p className="text-sm font-medium">{t('revert.affectedIds', { count: ids.length })}</p>
        <CopyButton value={ids.join('\n')} label={t('revert.copyIds')} className="size-7" />
      </div>
      <ul className="flex max-h-40 flex-wrap gap-x-3 gap-y-1 overflow-auto rounded-md border p-2 font-mono text-xs">
        {ids.map((id) => (
          <li key={id}>
            <Link to="/identities/$id" params={{ id }} className="hover:underline">
              {id}
            </Link>
          </li>
        ))}
      </ul>
    </div>
  );
}

export interface RevertActionsDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  defaultSite?: string;
}

/** Reverts automatic actions of a misfiring rule: dry run to list affected identities, then execute. */
export function RevertActionsDialog({ open, onOpenChange, defaultSite = '' }: RevertActionsDialogProps) {
  const { t } = useTranslation('identities');
  const { namespaceName } = useAuth();
  const invalidate = useInvalidateIdentityData();
  const [form, setForm] = useState<RevertForm>(() => defaultRevertForm(Date.now(), defaultSite));
  const [outcome, setOutcome] = useState<{
    response: RevertActionsResponse;
    dryRun: boolean;
    formKey: string;
  }>();
  const [confirmOpen, setConfirmOpen] = useState(false);
  const errors = validateRevertForm(form);
  const formKey = JSON.stringify(form);
  const namespace = namespaceName ?? '';

  const dryRun = useMutation({
    mutationFn: (vars: { form: RevertForm; key: string }) =>
      identityClient.revertActions(buildRevertRequest(namespace, vars.form, true)),
    onSuccess: (response, vars) => {
      setOutcome({ response, dryRun: true, formKey: vars.key });
      toast.info(t('revert.toastPreview', { count: response.result?.matched ?? 0 }));
    },
    onError: (err) => toast.error(errorMessage(err, t)),
  });

  const previewed = outcome?.dryRun === true && outcome.formKey === formKey;
  const matched = previewed ? (outcome.response.result?.matched ?? 0) : 0;
  const busy = dryRun.isPending || confirmOpen;

  const execute = async () => {
    const response = await identityClient.revertActions(buildRevertRequest(namespace, form, false));
    setOutcome({ response, dryRun: false, formKey });
    notifyBulkResult(t, t('revert.operationLabel'), response.result);
    invalidate();
  };

  return (
    <Dialog open={open} onOpenChange={(next) => !dryRun.isPending && onOpenChange(next)}>
      <DialogContent className="sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>{t('revert.title')}</DialogTitle>
          <DialogDescription>{t('revert.description')}</DialogDescription>
        </DialogHeader>
        <RevertActionsFields form={form} onChange={setForm} errors={errors} disabled={dryRun.isPending} />
        {outcome && (
          <section className="grid gap-3 rounded-md border bg-muted/20 p-3" aria-live="polite">
            <p className="text-sm font-medium">
              {outcome.dryRun ? t('revert.previewTitle') : t('revert.resultTitle')}
              {outcome.dryRun && !previewed && (
                <span className="ml-2 font-normal text-amber-600 dark:text-amber-400">
                  {t('revert.stale')}
                </span>
              )}
            </p>
            <BulkResultView result={outcome.response.result} />
            <AffectedIds ids={outcome.response.identityIds} />
          </section>
        )}
        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)} disabled={dryRun.isPending}>
            {t('common:actions.close')}
          </Button>
          <PermissionButton
            permission={PERMISSIONS.identityOperate}
            site={form.site || undefined}
            variant="outline"
            disabled={hasFormErrors(errors) || !namespace || busy}
            onClick={() => dryRun.mutate({ form, key: formKey })}
          >
            {dryRun.isPending ? <LoaderCircleIcon className="animate-spin" /> : <FlaskConicalIcon />}
            {t('revert.dryRun')}
          </PermissionButton>
          <PermissionButton
            permission={PERMISSIONS.identityOperate}
            site={form.site || undefined}
            variant="destructive"
            disabled={!previewed || matched === 0 || busy}
            title={previewed ? undefined : t('revert.dryRunFirst')}
            onClick={() => setConfirmOpen(true)}
          >
            <Undo2Icon />
            {t('revert.execute')}
          </PermissionButton>
        </DialogFooter>
        <ConfirmDialog
          open={confirmOpen}
          onOpenChange={setConfirmOpen}
          title={t('revert.confirmTitle')}
          description={t('revert.confirmDescription')}
          destructive
          affectedCount={matched}
          confirmText="revert"
          confirmLabel={t('revert.execute')}
          onConfirm={execute}
        />
      </DialogContent>
    </Dialog>
  );
}
