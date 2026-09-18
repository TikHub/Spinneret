import { useTranslation } from 'react-i18next';

import { Button } from '@/components/ui/button';
import { FormField } from '@/components/ui/form';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { cn } from '@/lib/utils';

import { type ScopeDepth } from '../../selectors';
import { useEndpointGroupOptions, useSiteOptions, type ScopeSelection } from '../../usePolicyQueries';
import { NONE } from '../fields';

export interface ScopeSelectsProps {
  value: ScopeSelection;
  onChange: (value: ScopeSelection) => void;
  /** Deepest level that must be selected ("namespace" = everything optional). */
  required?: ScopeDepth;
  disabled?: boolean;
  className?: string;
  /** Hide the endpoint group select. */
  hideEndpointGroup?: boolean;
}

const DEPTH: Record<ScopeDepth, number> = { namespace: 0, site: 1, client: 2, endpoint_group: 3 };

/** Cascading site → client → endpoint group selects (lists from SiteAdminService). */
export function ScopeSelects({
  value,
  onChange,
  required = 'namespace',
  disabled,
  className,
  hideEndpointGroup,
}: ScopeSelectsProps) {
  const { t } = useTranslation('policies');
  const sites = useSiteOptions();
  const groups = useEndpointGroupOptions(value.site, value.client);
  const site = sites.data?.sites.find((s) => s.name === value.site);
  const depth = DEPTH[required];

  return (
    <div className={cn('grid gap-3 sm:grid-cols-3', className)}>
      <FormField
        label={t('fields.site')}
        required={depth >= 1}
        error={
          sites.isError ? (
            <span className="inline-flex items-center gap-1">
              {t('scope.sitesFailed')}
              <Button
                variant="link"
                size="sm"
                className="h-auto p-0 text-xs"
                onClick={() => void sites.refetch()}
              >
                {t('common:actions.retry')}
              </Button>
            </span>
          ) : undefined
        }
      >
        <Select
          value={value.site || (depth < 1 ? NONE : '')}
          onValueChange={(v) => onChange({ site: v === NONE ? '' : v, client: '', endpointGroup: '' })}
          disabled={disabled || sites.isLoading}
        >
          <SelectTrigger className="w-full">
            <SelectValue placeholder={sites.isLoading ? t('scope.loading') : t('scope.pickSite')} />
          </SelectTrigger>
          <SelectContent>
            {depth < 1 && <SelectItem value={NONE}>{t('scope.allSites')}</SelectItem>}
            {sites.data?.sites.map((s) => (
              <SelectItem key={s.id} value={s.name}>
                {s.displayName ? `${s.displayName} (${s.name})` : s.name}
              </SelectItem>
            ))}
            {value.site && !site && <SelectItem value={value.site}>{value.site}</SelectItem>}
          </SelectContent>
        </Select>
      </FormField>
      <FormField label={t('fields.client')} required={depth >= 2}>
        <Select
          value={value.client || (depth < 2 ? NONE : '')}
          onValueChange={(v) => onChange({ ...value, client: v === NONE ? '' : v, endpointGroup: '' })}
          disabled={disabled || !value.site}
        >
          <SelectTrigger className="w-full">
            <SelectValue placeholder={t('scope.pickClient')} />
          </SelectTrigger>
          <SelectContent>
            {depth < 2 && <SelectItem value={NONE}>{t('scope.allClients')}</SelectItem>}
            {(site?.clients ?? []).map((c) => (
              <SelectItem key={c} value={c}>
                {c}
              </SelectItem>
            ))}
            {value.client && !site?.clients.includes(value.client) && (
              <SelectItem value={value.client}>{value.client}</SelectItem>
            )}
          </SelectContent>
        </Select>
      </FormField>
      {!hideEndpointGroup && (
        <FormField
          label={t('fields.endpointGroup')}
          required={depth >= 3}
          error={groups.isError ? t('scope.groupsFailed') : undefined}
        >
          <Select
            value={value.endpointGroup || (depth < 3 ? NONE : '')}
            onValueChange={(v) => onChange({ ...value, endpointGroup: v === NONE ? '' : v })}
            disabled={disabled || !value.client || groups.isLoading}
          >
            <SelectTrigger className="w-full">
              <SelectValue placeholder={groups.isLoading ? t('scope.loading') : t('scope.pickGroup')} />
            </SelectTrigger>
            <SelectContent>
              {depth < 3 && <SelectItem value={NONE}>{t('scope.allGroups')}</SelectItem>}
              {groups.data?.endpointGroups.map((g) => (
                <SelectItem key={g.id} value={g.name}>
                  {g.name}
                </SelectItem>
              ))}
              {value.endpointGroup &&
                !groups.data?.endpointGroups.some((g) => g.name === value.endpointGroup) && (
                  <SelectItem value={value.endpointGroup}>{value.endpointGroup}</SelectItem>
                )}
            </SelectContent>
          </Select>
        </FormField>
      )}
    </div>
  );
}
