import { ClipboardPasteIcon } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';

import { Button } from '@/components/ui/button';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { FormField } from '@/components/ui/form';
import { Textarea } from '@/components/ui/textarea';

import { ERROR_KINDS, OUTCOMES } from '../../constants';
import { reportFieldsFromJson, type DebugFieldError, type DebugReportFields } from '../../debug';
import { FieldSection, NumberField, SelectField, TagsField, TextField } from '../fields';

export interface ReportFieldsFormProps {
  value: DebugReportFields;
  onChange: (value: DebugReportFields) => void;
  errors: Record<string, DebugFieldError>;
  /** report.uri is required (the target is matched from it). */
  uriRequired: boolean;
}

/** Facts of the sample report (what a node would send to ReportService). */
export function ReportFieldsForm({ value, onChange, errors, uriRequired }: ReportFieldsFormProps) {
  const { t } = useTranslation('policies');
  const [pasteOpen, setPasteOpen] = useState(false);
  const [pasted, setPasted] = useState('');
  const [pasteError, setPasteError] = useState(false);
  const set = <K extends keyof DebugReportFields>(key: K, v: DebugReportFields[K]) =>
    onChange({ ...value, [key]: v });
  const err = (field: string) => (errors[field] ? t(`debugger.errors.${errors[field]}`) : undefined);
  const outcomeOptions = OUTCOMES.map((o) => ({
    value: o,
    label: <span className="font-mono text-xs">{o}</span>,
  }));
  const errorKinds = ERROR_KINDS.map((k) => ({
    value: k,
    label: <span className="font-mono text-xs">{k}</span>,
  }));

  const applyPaste = () => {
    const next = reportFieldsFromJson(pasted, value);
    if (!next) {
      setPasteError(true);
      return;
    }
    onChange(next);
    setPasteOpen(false);
    setPasted('');
    setPasteError(false);
  };

  return (
    <FieldSection
      title={
        <span className="flex items-center justify-between gap-2">
          {t('debugger.reportTitle')}
          <Button variant="outline" size="sm" onClick={() => setPasteOpen(true)}>
            <ClipboardPasteIcon />
            {t('debugger.paste')}
          </Button>
        </span>
      }
      description={t('debugger.reportHint')}
    >
      <TextField
        label={t('fields.reportUri')}
        value={value.uri}
        onChange={(v) => set('uri', v)}
        placeholder="/api/search"
        mono
        maxLength={2048}
        required={uriRequired}
        error={err('report.uri')}
      />
      <TextField
        label={t('fields.method')}
        value={value.method}
        onChange={(v) => set('method', v.toUpperCase())}
        mono
        maxLength={16}
      />
      <NumberField
        label={t('fields.httpStatus')}
        integer
        value={value.httpStatus}
        onChange={(v) => set('httpStatus', v)}
        description={t('debugger.httpStatusHint')}
        error={err('report.httpStatus')}
      />
      <TextField
        label={t('fields.businessCode')}
        value={value.businessCode}
        onChange={(v) => set('businessCode', v)}
        mono
        maxLength={64}
      />
      <SelectField
        label={t('fields.errorKind')}
        value={value.errorKind}
        onChange={(v) => set('errorKind', v)}
        options={errorKinds}
        noneLabel={t('debugger.noError')}
      />
      <SelectField
        label={t('fields.outcomeHint')}
        value={value.outcomeHint}
        onChange={(v) => set('outcomeHint', v)}
        options={outcomeOptions}
        noneLabel={t('debugger.noHint')}
      />
      <TagsField
        label={t('fields.markers')}
        value={value.markers}
        onChange={(v) => set('markers', v)}
        maxTags={32}
        validate={(tag) => tag.length <= 64}
        error={err('report.markers')}
        className="sm:col-span-2"
      />
      <div className="grid grid-cols-2 gap-3">
        <NumberField
          label={t('fields.latencyMs')}
          integer
          value={value.latencyMs}
          onChange={(v) => set('latencyMs', v)}
          error={err('report.latencyMs')}
        />
        <NumberField
          label={t('fields.responseBytes')}
          integer
          value={value.responseBytes}
          onChange={(v) => set('responseBytes', v)}
          error={err('report.responseBytes')}
        />
      </div>

      <Dialog open={pasteOpen} onOpenChange={setPasteOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('debugger.pasteTitle')}</DialogTitle>
            <DialogDescription>{t('debugger.pasteDescription')}</DialogDescription>
          </DialogHeader>
          <FormField
            label={t('debugger.pasteLabel')}
            error={pasteError ? t('debugger.pasteInvalid') : undefined}
          >
            <Textarea
              value={pasted}
              onChange={(e) => {
                setPasted(e.target.value);
                setPasteError(false);
              }}
              rows={10}
              className="font-mono text-xs"
              spellCheck={false}
              placeholder='{"uri": "/api/search", "http_status": 429, "markers": ["captcha_page"]}'
            />
          </FormField>
          <DialogFooter>
            <Button variant="outline" onClick={() => setPasteOpen(false)}>
              {t('common:actions.cancel')}
            </Button>
            <Button onClick={applyPaste} disabled={pasted.trim() === ''}>
              {t('debugger.pasteApply')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </FieldSection>
  );
}
