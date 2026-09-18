import { useTranslation } from 'react-i18next';

import { ERROR_KINDS } from '../../constants';
import { type HttpStatusMode, type SignalWhenModel, type UriMode } from '../../model/signal';
import { CheckboxGroup, SelectField, TagsField, TextField } from '../fields';
import { RangeField } from './RangeField';

export interface SignalWhenEditorProps {
  value: SignalWhenModel;
  onChange: (value: SignalWhenModel) => void;
  disabled?: boolean;
}

const HTTP_STATUS = /^\d{1,3}$/;
const METHOD = /^[A-Za-z]{1,16}$/;

/** Conditions of a signal rule; all set conditions must match (logical AND). */
export function SignalWhenEditor({ value, onChange, disabled }: SignalWhenEditorProps) {
  const { t } = useTranslation('policies');
  const set = <K extends keyof SignalWhenModel>(key: K, v: SignalWhenModel[K]) =>
    onChange({ ...value, [key]: v });
  const errorKinds = ERROR_KINDS.map((k) => ({
    value: k,
    label: <span className="font-mono text-xs">{k}</span>,
  }));

  return (
    <div className="grid gap-4">
      <p className="text-xs text-muted-foreground">{t('signal.whenHint')}</p>
      <div className="grid gap-4 lg:grid-cols-2">
        <div className="grid content-start gap-2">
          <SelectField
            label={t('fields.httpStatus')}
            value={value.httpStatusMode}
            onChange={(v) => set('httpStatusMode', v as HttpStatusMode)}
            options={[
              { value: 'any', label: t('signal.statusModes.any') },
              { value: 'list', label: t('signal.statusModes.list') },
              { value: 'range', label: t('signal.statusModes.range') },
            ]}
            disabled={disabled}
          />
          {value.httpStatusMode === 'list' && (
            <TagsField
              label={t('signal.statusList')}
              description={t('signal.statusListHint')}
              value={value.httpStatusList}
              onChange={(v) => set('httpStatusList', v)}
              validate={(tag) => HTTP_STATUS.test(tag) && Number(tag) <= 999}
              placeholder="429"
              disabled={disabled}
            />
          )}
          {value.httpStatusMode === 'range' && (
            <RangeField
              label={t('signal.statusRange')}
              value={value.httpStatusRange}
              onChange={(v) => set('httpStatusRange', v)}
              disabled={disabled}
            />
          )}
        </div>
        <div className="grid content-start gap-2">
          <SelectField
            label={t('fields.uri')}
            value={value.uriMode}
            onChange={(v) =>
              onChange({ ...value, uriMode: v as UriMode, uriValue: v === 'any' ? '' : value.uriValue })
            }
            options={[
              { value: 'any', label: t('signal.uriModes.any') },
              { value: 'prefix', label: t('signal.uriModes.prefix') },
              { value: 'regex', label: t('signal.uriModes.regex') },
            ]}
            disabled={disabled}
          />
          {value.uriMode !== 'any' && (
            <TextField
              label={value.uriMode === 'prefix' ? t('signal.uriPrefix') : t('signal.uriRegex')}
              description={value.uriMode === 'prefix' ? t('signal.uriPrefixHint') : t('signal.uriRegexHint')}
              value={value.uriValue}
              onChange={(v) => set('uriValue', v)}
              placeholder={value.uriMode === 'prefix' ? '/api/' : '^/api/v\\d+/'}
              error={
                value.uriMode === 'prefix' && value.uriValue !== '' && !value.uriValue.startsWith('/')
                  ? t('signal.uriPrefixInvalid')
                  : undefined
              }
              mono
              maxLength={1024}
              disabled={disabled}
            />
          )}
        </div>
        <TagsField
          label={t('fields.businessCode')}
          description={t('signal.businessCodeHint')}
          value={value.businessCode}
          onChange={(v) => set('businessCode', v)}
          disabled={disabled}
        />
        <TagsField
          label={t('fields.markers')}
          description={t('signal.markersHint')}
          value={value.markers}
          onChange={(v) => set('markers', v)}
          placeholder="captcha_page"
          disabled={disabled}
        />
        <CheckboxGroup
          label={t('fields.errorKind')}
          value={value.errorKind}
          onChange={(v) => set('errorKind', v)}
          options={errorKinds}
          disabled={disabled}
        />
        <TagsField
          label={t('fields.method')}
          value={value.method}
          onChange={(v) =>
            set(
              'method',
              v.map((m) => m.toUpperCase()),
            )
          }
          validate={(tag) => METHOD.test(tag)}
          placeholder="GET"
          disabled={disabled}
        />
        <RangeField
          label={t('fields.latencyMs')}
          unit={t('units.ms')}
          value={value.latencyMs}
          onChange={(v) => set('latencyMs', v)}
          disabled={disabled}
        />
        <RangeField
          label={t('fields.responseBytes')}
          unit={t('units.bytes')}
          value={value.responseBytes}
          onChange={(v) => set('responseBytes', v)}
          disabled={disabled}
        />
      </div>
      {value.httpStatusMode === 'range' &&
        value.httpStatusRange.lower === '' &&
        value.httpStatusRange.upper === '' && (
          <p className="text-xs text-muted-foreground">{t('signal.emptyRangeIgnored')}</p>
        )}
    </div>
  );
}
