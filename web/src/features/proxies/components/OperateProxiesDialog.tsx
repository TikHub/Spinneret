import { TriangleAlertIcon } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';

import { ConfirmDialog } from '@/components/ConfirmDialog';
import { DurationInput } from '@/components/DurationInput';
import { FormField, FormStack } from '@/components/ui/form';
import { Input } from '@/components/ui/input';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { Textarea } from '@/components/ui/textarea';
import { type OperateProxiesResponse } from '@/gen/spinneret/v1/proxy_admin_pb';

import {
  buildOperateRequest,
  EMPTY_OPERATION_FORM,
  MAX_BULK_IDS,
  MAX_REASON_LENGTH,
  OPERATION_SPECS,
  requiresTypedConfirmation,
  validateOperation,
  type OperationForm,
  type ProxyOperation,
} from '../proxyOperations';
import { useOperateProxies, useSiteOptions } from '../useProxies';

/** Select value for "no site" (Radix Select items cannot use an empty value). */
const GLOBAL_SITE = '__global__';

export interface OperateProxiesDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  operation: ProxyOperation;
  ids: readonly string[];
  /** Display name for a single proxy. */
  subject?: string;
  onDone: (operation: ProxyOperation, ids: readonly string[], response: OperateProxiesResponse) => void;
}

/** Confirmation dialog of a manual proxy operation with duration, site and reason inputs. */
export function OperateProxiesDialog({
  open,
  onOpenChange,
  operation,
  ids,
  subject,
  onDone,
}: OperateProxiesDialogProps) {
  const { t } = useTranslation('proxies');
  const spec = OPERATION_SPECS[operation];
  const [form, setForm] = useState<OperationForm>(EMPTY_OPERATION_FORM);
  const [touched, setTouched] = useState(false);
  const mutation = useOperateProxies();
  const sites = useSiteOptions(open && spec.siteScoped);

  const errors = validateOperation(operation, ids, form);
  const update = (patch: Partial<OperationForm>) => {
    setForm((prev) => ({ ...prev, ...patch }));
    setTouched(true);
  };
  const durationError = errors.find((e) => e.startsWith('duration_'));
  const opLabel = t(`operations.${operation}`);
  const typed = requiresTypedConfirmation(operation, form);

  const confirm = async () => {
    const request = buildOperateRequest(operation, ids, form);
    const response = await mutation.mutateAsync(request);
    onDone(operation, request.ids, response);
  };

  return (
    <ConfirmDialog
      open={open}
      onOpenChange={onOpenChange}
      title={
        subject
          ? t('operate.titleOne', { operation: opLabel, name: subject })
          : t('operate.title', { operation: opLabel, count: ids.length })
      }
      description={t(`operationDescriptions.${operation}`)}
      affectedCount={subject ? undefined : ids.length}
      destructive={spec.destructive}
      confirmLabel={opLabel}
      confirmText={typed ? operation : undefined}
      confirmDisabled={errors.length > 0}
      onConfirm={confirm}
    >
      <FormStack>
        {spec.siteScoped && (
          <FormField label={t('operate.site')} description={t('operate.siteHint')}>
            {sites.isError ? (
              <Input
                value={form.site}
                onChange={(e) => update({ site: e.target.value })}
                placeholder={t('operate.siteNamePlaceholder')}
                maxLength={64}
              />
            ) : (
              <Select
                value={form.site === '' ? GLOBAL_SITE : form.site}
                onValueChange={(v) => update({ site: v === GLOBAL_SITE ? '' : v })}
              >
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={GLOBAL_SITE}>{t('operate.siteGlobal')}</SelectItem>
                  {(sites.data ?? []).map((site) => (
                    <SelectItem key={site.name} value={site.name}>
                      {site.label}
                      {site.label !== site.name && (
                        <span className="font-mono text-xs text-muted-foreground">{site.name}</span>
                      )}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            )}
          </FormField>
        )}
        {spec.duration !== 'none' && (
          <FormField
            label={t('operate.duration')}
            required={spec.duration === 'required'}
            description={
              spec.duration === 'optional'
                ? t('operate.durationOptionalHint')
                : spec.allowPermanent
                  ? t('operate.durationPermanentHint')
                  : undefined
            }
            error={
              touched &&
              durationError &&
              durationError !== 'duration_invalid' &&
              durationError !== 'duration_permanent'
                ? t(`operate.errors.${durationError}`)
                : undefined
            }
          >
            <DurationInput
              value={form.duration}
              onChange={(duration) => update({ duration })}
              allowPermanent={spec.allowPermanent}
              allowEmpty={spec.duration === 'optional'}
            />
          </FormField>
        )}
        {typed && operation === 'ban' && (
          <p className="flex items-center gap-2 text-sm text-destructive">
            <TriangleAlertIcon className="size-4 shrink-0" aria-hidden />
            {t('operate.permanentWarning')}
          </p>
        )}
        <FormField
          label={t('operate.reason')}
          error={
            errors.includes('reason_too_long')
              ? t('operate.errors.reason_too_long', { max: MAX_REASON_LENGTH })
              : undefined
          }
        >
          <Textarea
            value={form.reason}
            onChange={(e) => update({ reason: e.target.value })}
            placeholder={t('operate.reasonPlaceholder')}
            rows={2}
          />
        </FormField>
        {errors.includes('too_many_ids') && (
          <p role="alert" className="text-sm text-destructive">
            {t('operate.errors.too_many_ids', { max: MAX_BULK_IDS })}
          </p>
        )}
      </FormStack>
    </ConfirmDialog>
  );
}
