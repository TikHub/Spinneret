import { useState } from 'react';
import { useTranslation } from 'react-i18next';

import { DurationInput } from '@/components/DurationInput';
import { FormField, FormStack } from '@/components/ui/form';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { Switch } from '@/components/ui/switch';
import { Textarea } from '@/components/ui/textarea';

import {
  MAX_REASON_LENGTH,
  operationTraits,
  type OperationForm,
  type OperationFormErrors,
  type OperationScope,
} from '../operations';
import { EndpointGroupSelect } from './EndpointGroupSelect';
import { ALL_VALUE, SiteSelect } from './SiteSelect';

export interface OperationFieldsProps {
  form: OperationForm;
  onChange: (form: OperationForm) => void;
  errors: OperationFormErrors;
  /** Site of the affected identities when they share one; otherwise a site picker is offered for endpoint groups. */
  site?: string;
}

/** Inputs of a manual identity operation: scope, endpoint group, duration, reset flags and reason. */
export function OperationFields({ form, onChange, errors, site }: OperationFieldsProps) {
  const { t } = useTranslation('identities');
  const traits = operationTraits(form.operation);
  const [pickedSite, setPickedSite] = useState(site ?? '');
  const groupSite = site ?? pickedSite;
  const set = (patch: Partial<OperationForm>) => onChange({ ...form, ...patch });

  const scopes: OperationScope[] =
    traits.scope === 'cooldown'
      ? ['identity_site', 'identity_endpoint']
      : ['', 'identity_site', 'identity_endpoint'];

  return (
    <FormStack className="gap-3">
      {traits.scope !== 'none' && (
        <FormField
          label={t('operation.scope')}
          description={t(`operation.scopeHints.${form.scope || 'all'}`)}
        >
          <Select
            value={form.scope || ALL_VALUE}
            onValueChange={(v) =>
              set({ scope: (v === ALL_VALUE ? '' : v) as OperationScope, endpointGroupId: '' })
            }
          >
            <SelectTrigger className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {scopes.map((scope) => (
                <SelectItem key={scope || ALL_VALUE} value={scope || ALL_VALUE}>
                  {t(`operation.scopes.${scope || 'all'}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </FormField>
      )}
      {traits.scope !== 'none' && form.scope === 'identity_endpoint' && (
        <div className="grid gap-3 sm:grid-cols-2">
          {site === undefined && (
            <FormField label={t('fields.site')} required>
              <SiteSelect
                value={pickedSite}
                onChange={(next) => {
                  setPickedSite(next);
                  set({ endpointGroupId: '' });
                }}
              />
            </FormField>
          )}
          <FormField
            label={t('fields.endpointGroup')}
            required
            error={errors.endpointGroupId ? t('operation.errors.groupRequired') : undefined}
            className={site === undefined ? undefined : 'sm:col-span-2'}
          >
            <EndpointGroupSelect
              site={groupSite}
              value={form.endpointGroupId}
              onChange={(endpointGroupId) => set({ endpointGroupId })}
            />
          </FormField>
        </div>
      )}
      {traits.duration !== 'none' && (
        <FormField
          label={t('operation.duration')}
          required={traits.duration === 'required'}
          description={t(
            `operation.durationHints.${form.operation === 'quarantine' ? 'quarantine' : form.operation === 'ban' ? 'ban' : 'cooldown'}`,
          )}
          error={
            errors.duration === 'required' && form.duration.trim() !== ''
              ? t('operation.errors.durationPositive')
              : undefined
          }
        >
          <DurationInput
            value={form.duration}
            onChange={(duration) => set({ duration })}
            allowPermanent={traits.allowPermanent}
            allowEmpty={traits.duration === 'optional'}
          />
        </FormField>
      )}
      {traits.resetFlags && (
        <div className="grid gap-2 rounded-md border p-3">
          <FormField inline label={t('operation.resetFailures')}>
            <Switch checked={form.resetFailures} onCheckedChange={(v) => set({ resetFailures: v })} />
          </FormField>
          <FormField inline label={t('operation.resetHealth')}>
            <Switch checked={form.resetHealth} onCheckedChange={(v) => set({ resetHealth: v })} />
          </FormField>
        </div>
      )}
      <FormField
        label={t('operation.reason')}
        description={t('operation.reasonHint')}
        error={errors.reason ? t('operation.errors.reasonTooLong', { max: MAX_REASON_LENGTH }) : undefined}
      >
        <Textarea
          value={form.reason}
          onChange={(e) => set({ reason: e.target.value })}
          rows={2}
          maxLength={MAX_REASON_LENGTH + 1}
          placeholder={t('operation.reasonPlaceholder')}
        />
      </FormField>
    </FormStack>
  );
}
