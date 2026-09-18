import { CalendarRangeIcon } from 'lucide-react';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';

import { Button } from '@/components/ui/button';
import { FormField } from '@/components/ui/form';
import { Input } from '@/components/ui/input';
import { Popover, PopoverAnchor, PopoverContent } from '@/components/ui/popover';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { formatDateTime, HOUR_MS } from '@/lib/time';

import {
  CUSTOM_RANGE,
  fromDateTimeLocal,
  isValidCustomRange,
  presetSelection,
  TIME_RANGE_PRESETS,
  toDateTimeLocal,
  type TimeRangePreset,
  type TimeRangeSelection,
} from './timeRange';

export interface TimeRangePickerProps {
  value: TimeRangeSelection;
  onChange: (value: TimeRangeSelection) => void;
}

interface Draft {
  from: string;
  to: string;
}

/** Preset select ("last 1 hour", ...) with a custom absolute range popover. */
export function TimeRangePicker({ value, onChange }: TimeRangePickerProps) {
  const { t } = useTranslation('requests');
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState<Draft>({ from: '', to: '' });
  const [submitted, setSubmitted] = useState(false);

  const openCustom = (deferred: boolean) => {
    const now = Date.now();
    const from = value.kind === 'custom' ? value.from : now - HOUR_MS;
    const to = value.kind === 'custom' ? value.to : now;
    setDraft({ from: toDateTimeLocal(from), to: toDateTimeLocal(to) });
    setSubmitted(false);
    // Opened from the select: wait until the select has closed and restored focus.
    if (deferred) setTimeout(() => setOpen(true), 0);
    else setOpen(true);
  };

  const fromMs = fromDateTimeLocal(draft.from);
  const toMs = fromDateTimeLocal(draft.to);
  const valid = fromMs !== undefined && toMs !== undefined && isValidCustomRange(fromMs, toMs);

  const apply = (event: FormEvent) => {
    event.preventDefault();
    setSubmitted(true);
    if (fromMs === undefined || toMs === undefined || !isValidCustomRange(fromMs, toMs)) return;
    onChange({ kind: 'custom', from: fromMs, to: toMs });
    setOpen(false);
  };

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverAnchor asChild>
        <div className="flex items-center gap-1">
          <Select
            value={value.kind === 'custom' ? CUSTOM_RANGE : value.preset}
            onValueChange={(v) => {
              if (v === CUSTOM_RANGE) openCustom(true);
              else onChange(presetSelection(v as TimeRangePreset));
            }}
          >
            <SelectTrigger size="sm" className="w-40" aria-label={t('shared.timeRange')}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {TIME_RANGE_PRESETS.map((preset) => (
                <SelectItem key={preset} value={preset}>
                  {t(`shared.presets.${preset}`)}
                </SelectItem>
              ))}
              <SelectItem value={CUSTOM_RANGE}>{t('shared.presets.custom')}</SelectItem>
            </SelectContent>
          </Select>
          {value.kind === 'custom' && (
            <Button
              variant="outline"
              size="sm"
              className="tabular"
              onClick={() => openCustom(false)}
              aria-label={t('shared.editRange')}
            >
              <CalendarRangeIcon />
              {t('shared.customLabel', {
                from: formatDateTime(value.from, false),
                to: formatDateTime(value.to, false),
              })}
            </Button>
          )}
        </div>
      </PopoverAnchor>
      <PopoverContent align="start" className="w-80" onFocusOutside={(event) => event.preventDefault()}>
        <form className="grid gap-3" onSubmit={apply} noValidate>
          <FormField label={t('shared.from')} required>
            <Input
              type="datetime-local"
              value={draft.from}
              onChange={(e) => setDraft((d) => ({ ...d, from: e.target.value }))}
            />
          </FormField>
          <FormField
            label={t('shared.to')}
            required
            error={submitted && !valid ? t('shared.invalidRange') : undefined}
          >
            <Input
              type="datetime-local"
              value={draft.to}
              onChange={(e) => setDraft((d) => ({ ...d, to: e.target.value }))}
            />
          </FormField>
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" size="sm" onClick={() => setOpen(false)}>
              {t('common:actions.cancel')}
            </Button>
            <Button type="submit" size="sm">
              {t('common:actions.apply')}
            </Button>
          </div>
        </form>
      </PopoverContent>
    </Popover>
  );
}
