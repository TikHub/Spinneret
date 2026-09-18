import { useId, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';

import { Input } from '@/components/ui/input';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';

import { type RangeModel } from '../../model/signal';

export interface RangeFieldProps {
  label: ReactNode;
  value: RangeModel;
  onChange: (value: RangeModel) => void;
  unit?: string;
  disabled?: boolean;
}

const INT = /^\d*$/;

/** Integer range editor: lower bound (≥ or >) and upper bound (≤ or <); empty bounds are open. */
export function RangeField({ label, value, onChange, unit, disabled }: RangeFieldProps) {
  const { t } = useTranslation('policies');
  const id = useId();
  const invalid = !INT.test(value.lower.trim()) || !INT.test(value.upper.trim());
  return (
    <fieldset className="grid content-start gap-1.5" disabled={disabled}>
      <legend className="mb-1.5 text-sm leading-none font-medium">
        {label}
        {unit && <span className="ml-1 font-normal text-muted-foreground">({unit})</span>}
      </legend>
      <div className="grid grid-cols-[4.5rem_1fr_4.5rem_1fr] items-center gap-1.5">
        <Select
          value={value.lowerOp}
          onValueChange={(v) => onChange({ ...value, lowerOp: v as RangeModel['lowerOp'] })}
        >
          <SelectTrigger size="sm" className="w-full font-mono" aria-label={t('range.lowerOp')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="gte">≥</SelectItem>
            <SelectItem value="gt">&gt;</SelectItem>
          </SelectContent>
        </Select>
        <Input
          id={`${id}-lower`}
          value={value.lower}
          onChange={(e) => onChange({ ...value, lower: e.target.value })}
          inputMode="numeric"
          placeholder={t('range.open')}
          aria-label={t('range.lower')}
          aria-invalid={!INT.test(value.lower.trim())}
          className="h-8 font-mono"
        />
        <Select
          value={value.upperOp}
          onValueChange={(v) => onChange({ ...value, upperOp: v as RangeModel['upperOp'] })}
        >
          <SelectTrigger size="sm" className="w-full font-mono" aria-label={t('range.upperOp')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="lte">≤</SelectItem>
            <SelectItem value="lt">&lt;</SelectItem>
          </SelectContent>
        </Select>
        <Input
          id={`${id}-upper`}
          value={value.upper}
          onChange={(e) => onChange({ ...value, upper: e.target.value })}
          inputMode="numeric"
          placeholder={t('range.open')}
          aria-label={t('range.upper')}
          aria-invalid={!INT.test(value.upper.trim())}
          className="h-8 font-mono"
        />
      </div>
      {invalid && (
        <p role="alert" className="text-xs text-destructive">
          {t('builder.invalidInteger')}
        </p>
      )}
    </fieldset>
  );
}
