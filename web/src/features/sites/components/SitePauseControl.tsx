import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import { PERMISSIONS } from '@/app/auth/permissions';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { FormField } from '@/components/ui/form';
import { Switch } from '@/components/ui/switch';
import { Textarea } from '@/components/ui/textarea';
import { SimpleTooltip } from '@/components/ui/tooltip';
import { type Site } from '@/gen/spinneret/v1/site_admin_pb';

import { useSitePermissions } from '../useSitePermissions';
import { useSetSitePaused } from '../useSites';

/** Maximum reason length (SetSitePausedRequest.reason). */
const MAX_REASON = 1024;

export interface SitePauseControlProps {
  site: Site;
  onDialogChange?: (open: boolean) => void;
}

/** Site switch: pausing refuses every lease of the site; both directions need a confirmation. */
export function SitePauseControl({ site, onDialogChange }: SitePauseControlProps) {
  const { t } = useTranslation('sites');
  const { canPauseSite } = useSitePermissions();
  const allowed = canPauseSite(site.id);
  const [pending, setPending] = useState<boolean>();
  const [reason, setReason] = useState('');
  const mutation = useSetSitePaused();

  const openConfirm = (paused: boolean) => {
    setReason('');
    setPending(paused);
    onDialogChange?.(true);
  };
  const close = (open: boolean) => {
    if (open) return;
    setPending(undefined);
    onDialogChange?.(false);
  };

  const confirm = async () => {
    const paused = pending === true;
    await mutation.mutateAsync({ site: site.name, paused, reason: paused ? reason.trim() : '' });
    toast.success(paused ? t('pause.paused', { name: site.name }) : t('pause.resumed', { name: site.name }));
  };

  const control = (
    <label className="inline-flex items-center gap-2 text-sm">
      {/*
        The switch is on while the site is running, like the site switch on the
        breakers page, and its label names the action it triggers so its state is
        unambiguous for assistive technology.
      */}
      <Switch
        checked={!site.paused}
        onCheckedChange={(next) => openConfirm(!next)}
        disabled={!allowed || mutation.isPending}
        aria-label={
          site.paused
            ? t('pause.resumeLabel', { name: site.name })
            : t('pause.pauseLabel', { name: site.name })
        }
      />
      <span className={site.paused ? 'font-medium text-amber-700 dark:text-amber-400' : undefined}>
        {site.paused ? t('pause.pausedLabel') : t('pause.activeLabel')}
      </span>
    </label>
  );

  return (
    <>
      {allowed ? (
        control
      ) : (
        <SimpleTooltip content={t('common:permission.missing', { permission: PERMISSIONS.breakerOperate })}>
          <span tabIndex={0} className="inline-flex">
            {control}
          </span>
        </SimpleTooltip>
      )}
      <ConfirmDialog
        open={pending !== undefined}
        onOpenChange={close}
        destructive={pending === true}
        title={
          pending ? t('pause.pauseTitle', { name: site.name }) : t('pause.resumeTitle', { name: site.name })
        }
        description={pending ? t('pause.pauseDescription') : t('pause.resumeDescription')}
        confirmLabel={pending ? t('pause.pause') : t('pause.resume')}
        confirmDisabled={reason.length > MAX_REASON}
        onConfirm={confirm}
      >
        {pending && (
          <FormField
            label={t('pause.reason')}
            error={reason.length > MAX_REASON ? t('form.errors.tooLong', { max: MAX_REASON }) : undefined}
          >
            <Textarea
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              placeholder={t('pause.reasonPlaceholder')}
              rows={2}
            />
          </FormField>
        )}
      </ConfirmDialog>
    </>
  );
}
