import { TriangleAlertIcon } from 'lucide-react';
import { useId, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import { ConfirmDialog } from '@/components/ConfirmDialog';
import { Checkbox } from '@/components/ui/checkbox';
import { Label } from '@/components/ui/label';
import { type Site } from '@/gen/spinneret/v1/site_admin_pb';
import { formatNumber } from '@/lib/format';

import { useDeleteSite } from '../useSites';

export interface DeleteSiteDialogProps {
  site: Site;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onDeleted: (site: Site) => void;
}

/**
 * Deletes a site with its endpoint groups, identity types, identities and
 * accounts. Requires typing the site name; a site with identities also needs
 * the explicit "force" checkbox.
 */
export function DeleteSiteDialog({ site, open, onOpenChange, onDeleted }: DeleteSiteDialogProps) {
  const { t, i18n } = useTranslation('sites');
  const [force, setForce] = useState(false);
  const forceId = useId();
  const mutation = useDeleteSite();
  const hasIdentities = site.identityCount > 0;

  const confirm = async () => {
    await mutation.mutateAsync({ id: site.id, force: hasIdentities && force });
    toast.success(t('delete.done', { name: site.name }));
    onDeleted(site);
  };

  return (
    <ConfirmDialog
      open={open}
      onOpenChange={onOpenChange}
      destructive
      title={t('delete.title', { name: site.displayName || site.name })}
      description={t('delete.description')}
      confirmLabel={t('common:actions.delete')}
      confirmText={site.name}
      confirmDisabled={hasIdentities && !force}
      onConfirm={confirm}
    >
      {hasIdentities && (
        <div className="grid gap-2 rounded-md border border-destructive/40 bg-destructive/5 p-3">
          <p className="flex gap-2 text-sm font-medium text-destructive">
            <TriangleAlertIcon className="mt-0.5 size-4 shrink-0" aria-hidden />
            {t('delete.identitiesWarning', {
              count: site.identityCount,
              formatted: formatNumber(site.identityCount, undefined, i18n.language),
            })}
          </p>
          <div className="flex items-start gap-2">
            <Checkbox
              id={forceId}
              checked={force}
              onCheckedChange={(checked) => setForce(checked === true)}
              className="mt-0.5"
            />
            <Label htmlFor={forceId} className="leading-snug font-normal">
              {t('delete.force')}
            </Label>
          </div>
        </div>
      )}
    </ConfirmDialog>
  );
}
