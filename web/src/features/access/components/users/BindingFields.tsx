import { useId } from 'react';
import { useTranslation } from 'react-i18next';

import { useAuth } from '@/app/auth/AuthContext';
import { ROLE_PERMISSIONS } from '@/app/auth/permissions';
import { Checkbox } from '@/components/ui/checkbox';
import { FormField } from '@/components/ui/form';
import { Label } from '@/components/ui/label';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';

import { toKeySegment } from '../../forms';
import {
  EXTRA_PERMISSION_OPTIONS,
  redundantExtraPermissions,
  ROLES,
  withNamespace,
  type BindingErrors,
  type BindingFormValues,
  type ExtraPermission,
  type Role,
} from '../../userForm';

import { RoleHelpPopover } from './RoleHelpPopover';
import { SiteMultiSelect } from './SiteMultiSelect';

/** Select value standing for "all namespaces" (Radix Select items cannot use ""). */
const ALL_NAMESPACES = '__all__';

export interface BindingFieldsProps {
  value: BindingFormValues;
  onChange: (value: BindingFormValues) => void;
  errors: BindingErrors;
  disabled?: boolean;
}

/** Role, namespace, site and extra permission fields of a role binding. */
export function BindingFields({ value, onChange, errors, disabled }: BindingFieldsProps) {
  const { t } = useTranslation('access');
  const { namespaces } = useAuth();
  const groupId = useId();
  const names = namespaces.map((access) => access.namespace?.name ?? '').filter(Boolean);
  const namespaceOptions =
    value.namespace !== '' && !names.includes(value.namespace) ? [value.namespace, ...names] : names;
  const redundant = redundantExtraPermissions(value.role, value.extraPermissions, ROLE_PERMISSIONS);

  const toggleExtra = (permission: ExtraPermission, checked: boolean) =>
    onChange({
      ...value,
      extraPermissions: checked
        ? [...value.extraPermissions, permission]
        : value.extraPermissions.filter((p) => p !== permission),
    });

  return (
    <div className="grid gap-4">
      <div className="grid gap-4 sm:grid-cols-2">
        <div className="grid content-start gap-1.5">
          <div className="flex items-center gap-1">
            <Label htmlFor={`${groupId}-role`}>
              {t('bindings.role')}
              <span className="text-destructive">*</span>
            </Label>
            <RoleHelpPopover />
          </div>
          <FormField id={`${groupId}-role`} description={t(`roles.summaries.${value.role}`)}>
            <Select
              value={value.role}
              onValueChange={(role) => onChange({ ...value, role: role as Role })}
              disabled={disabled}
            >
              <SelectTrigger className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {ROLES.map((role) => (
                  <SelectItem key={role} value={role}>
                    {t(`roles.names.${role}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </FormField>
        </div>
        <FormField label={t('bindings.namespace')} description={t('bindings.namespaceHint')}>
          <Select
            value={value.namespace === '' ? ALL_NAMESPACES : value.namespace}
            onValueChange={(ns) => onChange(withNamespace(value, ns === ALL_NAMESPACES ? '' : ns))}
            disabled={disabled}
          >
            <SelectTrigger className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL_NAMESPACES}>{t('bindings.allNamespaces')}</SelectItem>
              {namespaceOptions.map((name) => (
                <SelectItem key={name} value={name} className="font-mono">
                  {name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </FormField>
      </div>

      {value.namespace !== '' ? (
        <FormField
          label={t('bindings.sites')}
          description={t('bindings.sitesHint', { namespace: value.namespace })}
          error={errors.sites && t(`bindings.errors.sites.${errors.sites}`)}
        >
          <SiteMultiSelect
            namespace={value.namespace}
            value={value.sites}
            onChange={(sites) => onChange({ ...value, sites })}
            disabled={disabled}
          />
        </FormField>
      ) : (
        errors.sites && (
          <p role="alert" className="text-xs text-destructive">
            {t(`bindings.errors.sites.${errors.sites}`)}
          </p>
        )
      )}

      <fieldset className="grid gap-2" disabled={disabled}>
        <legend className="mb-1 text-sm font-medium">{t('bindings.extraPermissions')}</legend>
        <div className="grid gap-2 sm:grid-cols-2">
          {EXTRA_PERMISSION_OPTIONS.map((permission) => {
            const id = `${groupId}-${permission}`;
            return (
              <div key={permission} className="flex items-start gap-2">
                <Checkbox
                  id={id}
                  checked={value.extraPermissions.includes(permission)}
                  onCheckedChange={(checked) => toggleExtra(permission, checked === true)}
                  className="mt-0.5"
                />
                <label htmlFor={id} className="grid text-sm">
                  <code className="font-mono text-xs">{permission}</code>
                  <span className="text-xs text-muted-foreground">
                    {t(`roles.extra.${toKeySegment(permission)}`)}
                  </span>
                </label>
              </div>
            );
          })}
        </div>
        {redundant.length > 0 && (
          <p className="text-xs text-muted-foreground">
            {t('bindings.redundantExtras', {
              role: t(`roles.names.${value.role}`),
              permissions: redundant.join(', '),
            })}
          </p>
        )}
        {errors.extraPermissions && (
          <p role="alert" className="text-xs text-destructive">
            {t('bindings.errors.extraPermissions')}
          </p>
        )}
      </fieldset>
    </div>
  );
}
