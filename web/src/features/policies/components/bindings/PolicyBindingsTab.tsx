import { LinkIcon } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';

import { PERMISSIONS } from '@/app/auth/permissions';
import { PermissionButton } from '@/app/auth/PermissionGate';
import { type Policy } from '@/gen/spinneret/v1/policy_admin_pb';

import { BindingsTable } from './BindingsTable';
import { SetBindingDialog } from './SetBindingDialog';

export interface PolicyBindingsTabProps {
  policy: Policy;
}

/** Bindings of the selected policy (visible to the caller) with bind and unbind actions. */
export function PolicyBindingsTab({ policy }: PolicyBindingsTabProps) {
  const { t } = useTranslation('policies');
  const [binding, setBinding] = useState(false);

  const bindButton = (
    <PermissionButton permission={PERMISSIONS.policyPublish} size="sm" onClick={() => setBinding(true)}>
      <LinkIcon />
      {t('bindings.bind')}
    </PermissionButton>
  );

  return (
    <div className="grid gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-sm text-muted-foreground">{t('bindings.policyHint')}</p>
        {bindButton}
      </div>
      <BindingsTable
        bindings={policy.bindings}
        emptyTitle={t('bindings.emptyPolicy')}
        emptyDescription={
          policy.currentVersion === 0 ? t('bindings.emptyUnpublished') : t('bindings.emptyPolicyDescription')
        }
      />
      <SetBindingDialog open={binding} onOpenChange={setBinding} policy={policy} />
    </div>
  );
}
