import { useTranslation } from 'react-i18next';

import { Badge } from '@/components/ui/badge';
import { SimpleTooltip } from '@/components/ui/tooltip';
import { type RoleBinding } from '@/gen/spinneret/v1/auth_pb';
import { cn } from '@/lib/utils';

/** Badge color per role (owner stands out, viewer is quiet). */
const ROLE_VARIANT: Record<string, 'default' | 'secondary' | 'outline' | 'muted'> = {
  owner: 'default',
  admin: 'secondary',
  operator: 'outline',
  viewer: 'muted',
};

export function RoleBadge({ role, className }: { role: string; className?: string }) {
  const { t } = useTranslation('access');
  return (
    <Badge variant={ROLE_VARIANT[role] ?? 'outline'} className={className}>
      {t(`roles.names.${role}`, { defaultValue: role })}
    </Badge>
  );
}

/** Where a binding applies: namespace and sites. */
function BindingScopeText({ binding }: { binding: RoleBinding }) {
  const { t } = useTranslation('access');
  if (binding.namespace === '') return <>{t('bindings.allNamespaces')}</>;
  const count = Math.max(binding.sites.length, binding.siteIds.length);
  const sites = count === 0 ? t('bindings.allSites') : t('bindings.sitesSelected', { count });
  return <>{`${binding.namespace} · ${sites}`}</>;
}

export interface BindingChipProps {
  binding: RoleBinding;
  /** Dim bindings of other tenants (all users mode). */
  foreign?: boolean;
}

/** One-line summary of a binding with the full details in a tooltip. */
export function BindingChip({ binding, foreign = false }: BindingChipProps) {
  const { t } = useTranslation('access');
  const details = (
    <span className="grid gap-0.5">
      {foreign && <span>{t('bindings.otherTenant', { tenant: binding.tenantId })}</span>}
      <span>
        {t('bindings.namespace')}: {binding.namespace || t('bindings.allNamespaces')}
      </span>
      <span>
        {t('bindings.sites')}: {binding.sites.length > 0 ? binding.sites.join(', ') : t('bindings.allSites')}
      </span>
      {binding.extraPermissions.length > 0 && (
        <span>
          {t('bindings.extraPermissions')}: {binding.extraPermissions.join(', ')}
        </span>
      )}
    </span>
  );
  return (
    <SimpleTooltip content={details}>
      <span
        tabIndex={0}
        className={cn(
          'inline-flex items-center gap-1 rounded-md border px-1 py-0.5 text-xs outline-none focus-visible:ring-2 focus-visible:ring-ring',
          foreign && 'border-dashed opacity-60',
        )}
      >
        <RoleBadge role={binding.role} className="px-1.5 py-0" />
        <span className="max-w-40 truncate text-muted-foreground">
          <BindingScopeText binding={binding} />
        </span>
        {binding.extraPermissions.length > 0 && (
          <span className="text-muted-foreground">+{binding.extraPermissions.length}</span>
        )}
      </span>
    </SimpleTooltip>
  );
}
