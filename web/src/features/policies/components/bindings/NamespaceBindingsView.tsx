import { LinkIcon, RefreshCwIcon } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';

import { PERMISSIONS } from '@/app/auth/permissions';
import { PermissionButton } from '@/app/auth/PermissionGate';
import { FilterBar } from '@/components/FilterBar';
import { Button } from '@/components/ui/button';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';

import { POLICY_KINDS } from '../../constants';
import { useBindings, useSiteOptions } from '../../usePolicyQueries';
import { NONE } from '../fields';
import { BindingsTable } from './BindingsTable';
import { SetBindingDialog } from './SetBindingDialog';

export interface NamespaceBindingsViewProps {
  onOpenPolicy: (policyId: string) => void;
}

/** All policy bindings of the namespace, filterable by kind and site. */
export function NamespaceBindingsView({ onOpenPolicy }: NamespaceBindingsViewProps) {
  const { t } = useTranslation('policies');
  const [kind, setKind] = useState('');
  const [site, setSite] = useState('');
  const [creating, setCreating] = useState(false);
  const bindings = useBindings({ kind, site });
  const sites = useSiteOptions();

  return (
    <div className="grid gap-3">
      <FilterBar
        activeCount={(kind ? 1 : 0) + (site ? 1 : 0)}
        onReset={() => {
          setKind('');
          setSite('');
        }}
        actions={
          <>
            <Button
              variant="outline"
              size="icon-sm"
              onClick={() => void bindings.refetch()}
              aria-label={t('common:actions.refresh')}
            >
              <RefreshCwIcon className={bindings.isFetching ? 'animate-spin' : undefined} />
            </Button>
            <PermissionButton
              permission={PERMISSIONS.policyPublish}
              size="sm"
              onClick={() => setCreating(true)}
            >
              <LinkIcon />
              {t('bindings.new')}
            </PermissionButton>
          </>
        }
      >
        <Select value={kind || NONE} onValueChange={(v) => setKind(v === NONE ? '' : v)}>
          <SelectTrigger size="sm" className="w-40" aria-label={t('fields.kind')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={NONE}>{t('list.allKinds')}</SelectItem>
            {POLICY_KINDS.map((k) => (
              <SelectItem key={k} value={k}>
                {t(`kinds.${k}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select value={site || NONE} onValueChange={(v) => setSite(v === NONE ? '' : v)}>
          <SelectTrigger size="sm" className="w-48" aria-label={t('fields.site')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={NONE}>{t('scope.allSites')}</SelectItem>
            {sites.data?.sites.map((s) => (
              <SelectItem key={s.id} value={s.name}>
                {s.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </FilterBar>
      <BindingsTable
        showPolicy
        onOpenPolicy={onOpenPolicy}
        bindings={bindings.data?.bindings}
        isLoading={bindings.isLoading}
        isFetching={bindings.isFetching}
        error={bindings.error}
        onRetry={() => void bindings.refetch()}
        emptyTitle={t('bindings.empty')}
        emptyDescription={t('bindings.emptyDescription')}
      />
      <SetBindingDialog open={creating} onOpenChange={setCreating} />
    </div>
  );
}
