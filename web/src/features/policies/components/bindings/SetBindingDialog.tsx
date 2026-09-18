import { LoaderCircleIcon, TriangleAlertIcon } from 'lucide-react';
import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import { useAuth } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';
import { Button } from '@/components/ui/button';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { FormField } from '@/components/ui/form';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { type Policy } from '@/gen/spinneret/v1/policy_admin_pb';
import { errorMessage } from '@/lib/errors';

import { POLICY_KINDS } from '../../constants';
import { useSetBinding } from '../../usePolicyMutations';
import { usePolicyOptions } from '../../usePolicyQueries';
import { LevelBadge } from '../labels';
import { bindingPermissionSite, EMPTY_SCOPE, scopeLevel } from '../../selectors';
import { ScopeSelects } from './ScopeSelects';

export interface SetBindingDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Fixed policy; when omitted the dialog offers a kind and policy picker. */
  policy?: Policy;
}

/** Binds a policy at namespace, site, client or endpoint group level (replacing the binding of the same kind). */
export function SetBindingDialog({ open, onOpenChange, policy }: SetBindingDialogProps) {
  const { t } = useTranslation('policies');
  const { can } = useAuth();
  const [scope, setScope] = useState(EMPTY_SCOPE);
  const [kind, setKind] = useState(policy?.kind ?? 'rotation');
  const [policyId, setPolicyId] = useState(policy?.id ?? '');
  const [wasOpen, setWasOpen] = useState(open);
  if (open !== wasOpen) {
    setWasOpen(open);
    if (open) {
      setScope(EMPTY_SCOPE);
      setKind(policy?.kind ?? 'rotation');
      setPolicyId(policy?.id ?? '');
    }
  }
  const options = usePolicyOptions(kind, open && !policy);
  const setBinding = useSetBinding();
  const selected = useMemo(
    () => policy ?? options.data?.policies.find((p) => p.id === policyId),
    [policy, options.data, policyId],
  );
  const level = scopeLevel(scope);
  const allowed = can(PERMISSIONS.policyPublish, bindingPermissionSite(scope.site));
  // The server refuses to bind a policy without a published version.
  const unpublished = selected !== undefined && selected.currentVersion === 0;

  const submit = () => {
    if (!selected || unpublished) return;
    setBinding.mutate(
      { policyId: selected.id, site: scope.site, client: scope.client, endpointGroup: scope.endpointGroup },
      {
        onSuccess: () => onOpenChange(false),
        onError: (err) => toast.error(t('toasts.bindFailed'), { description: errorMessage(err, t) }),
      },
    );
  };

  return (
    <Dialog open={open} onOpenChange={(next) => !setBinding.isPending && onOpenChange(next)}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>
            {policy ? t('bindings.bindTitle', { name: policy.name }) : t('bindings.newTitle')}
          </DialogTitle>
          <DialogDescription>{t('bindings.bindDescription')}</DialogDescription>
        </DialogHeader>
        <div className="grid gap-4">
          {!policy && (
            <div className="grid gap-3 sm:grid-cols-3">
              <FormField label={t('fields.kind')} required>
                <Select
                  value={kind}
                  onValueChange={(v) => {
                    setKind(v);
                    setPolicyId('');
                  }}
                >
                  <SelectTrigger className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {POLICY_KINDS.map((k) => (
                      <SelectItem key={k} value={k}>
                        {t(`kinds.${k}`)}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </FormField>
              <FormField label={t('fields.policy')} required className="sm:col-span-2">
                <Select value={policyId} onValueChange={setPolicyId} disabled={options.isLoading}>
                  <SelectTrigger className="w-full">
                    <SelectValue
                      placeholder={options.isLoading ? t('scope.loading') : t('bindings.pickPolicy')}
                    />
                  </SelectTrigger>
                  <SelectContent>
                    {options.data?.policies.map((p) => (
                      <SelectItem key={p.id} value={p.id}>
                        <span className="font-mono">{p.name}</span>
                        <span className="text-muted-foreground">
                          {p.currentVersion > 0 ? `v${p.currentVersion}` : t('list.unpublished')}
                        </span>
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </FormField>
            </div>
          )}
          <ScopeSelects value={scope} onChange={setScope} />
          <div className="flex flex-wrap items-center gap-2 text-sm">
            <span className="text-muted-foreground">{t('bindings.resultingLevel')}</span>
            <LevelBadge level={level} />
          </div>
          <p className="text-xs text-muted-foreground">
            {t('bindings.replaceHint', {
              kind: t(`kinds.${selected?.kind ?? kind}`, { defaultValue: kind }),
            })}
          </p>
          {unpublished && (
            <p
              role="alert"
              className="flex items-start gap-2 rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-sm"
            >
              <TriangleAlertIcon className="mt-0.5 size-4 shrink-0 text-amber-600" aria-hidden />
              {t('bindings.unpublishedWarning')}
            </p>
          )}
          {!allowed && <p className="text-xs text-destructive">{t('bindings.noPermission')}</p>}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={setBinding.isPending}>
            {t('common:actions.cancel')}
          </Button>
          <Button onClick={submit} disabled={!selected || unpublished || !allowed || setBinding.isPending}>
            {setBinding.isPending && <LoaderCircleIcon className="animate-spin" />}
            {t('bindings.bind')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
