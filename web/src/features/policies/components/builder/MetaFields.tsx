import { useMemo, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';

import { type PolicyMetaModel } from '../../model/common';
import { usePolicyOptions } from '../../usePolicyQueries';
import { FieldSection, SelectField, TextField } from '../fields';

export interface MetaFieldsProps<M extends PolicyMetaModel> {
  model: M;
  onChange: (model: M) => void;
  disabled?: boolean;
  /** Policy kind for the extends picker (signal and action only). */
  extendsKind?: 'signal' | 'action';
  extendsValue?: string;
  onExtendsChange?: (value: string) => void;
  children?: ReactNode;
}

/** Name (read-only), description, bind block summary and extends picker. */
export function MetaFields<M extends PolicyMetaModel>({
  model,
  onChange,
  disabled,
  extendsKind,
  extendsValue = '',
  onExtendsChange,
  children,
}: MetaFieldsProps<M>) {
  const { t } = useTranslation('policies');
  const options = usePolicyOptions(extendsKind ?? '', extendsKind !== undefined);
  const extendsOptions = useMemo(
    () =>
      (options.data?.policies ?? [])
        .filter((p) => p.name !== model.name)
        .map((p) => ({ value: p.name, label: p.name })),
    [options.data, model.name],
  );
  const bind = model.bind;

  return (
    <FieldSection title={t('builder.sections.general')}>
      <TextField
        label={t('fields.name')}
        value={model.name}
        onChange={() => undefined}
        disabled
        mono
        description={t('builder.nameReadOnly')}
      />
      <TextField
        label={t('fields.description')}
        value={model.description}
        onChange={(description) => onChange({ ...model, description })}
        disabled={disabled}
        maxLength={1024}
        className="xl:col-span-2"
      />
      {extendsKind && onExtendsChange && (
        <SelectField
          label={t('fields.extends')}
          description={t('builder.extendsHint')}
          value={extendsValue}
          onChange={onExtendsChange}
          options={extendsOptions}
          noneLabel={t('builder.none')}
          disabled={disabled}
        />
      )}
      {bind && (
        <div className="grid content-start gap-1.5 text-sm sm:col-span-2">
          <span className="font-medium">{t('fields.bind')}</span>
          <code className="w-fit rounded bg-muted px-2 py-1 font-mono text-xs">
            {[bind.site, bind.client, bind.endpointGroup].filter(Boolean).join(' / ')}
          </code>
          <p className="text-xs text-muted-foreground">{t('builder.bindHint')}</p>
        </div>
      )}
      {children}
    </FieldSection>
  );
}
