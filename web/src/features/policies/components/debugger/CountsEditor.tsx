import { PlusIcon, Trash2Icon } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';

import { COUNTER_SUBJECTS, OUTCOMES, type CounterSubject } from '../../constants';
import {
  counterKey,
  MAX_BAN_COUNTS,
  MAX_COUNTS,
  type BanCountEntry,
  type CountEntry,
  type CountError,
} from '../../debug';
import { newKey } from '../../model/common';

export interface CountsEditorProps {
  counts: readonly CountEntry[];
  banCounts: readonly BanCountEntry[];
  onCountsChange: (counts: CountEntry[]) => void;
  onBanCountsChange: (banCounts: BanCountEntry[]) => void;
  countErrors: Record<string, CountError>;
  banCountErrors: Record<string, CountError>;
}

/** Counter values ("<subject>:<outcome>:<window>" → count) and earlier bans per window. */
export function CountsEditor({
  counts,
  banCounts,
  onCountsChange,
  onBanCountsChange,
  countErrors,
  banCountErrors,
}: CountsEditorProps) {
  const { t } = useTranslation('policies');
  const updateCount = (key: string, patch: Partial<CountEntry>) =>
    onCountsChange(counts.map((c) => (c.key === key ? { ...c, ...patch } : c)));
  const updateBan = (key: string, patch: Partial<BanCountEntry>) =>
    onBanCountsChange(banCounts.map((c) => (c.key === key ? { ...c, ...patch } : c)));

  return (
    <section className="grid gap-4 rounded-lg border bg-card p-4 lg:grid-cols-[2fr_1fr]">
      <div className="grid content-start gap-2">
        <div className="space-y-0.5">
          <h3 className="text-sm font-semibold">{t('debugger.countsTitle')}</h3>
          <p className="text-xs text-muted-foreground">{t('debugger.countsHint')}</p>
        </div>
        {counts.map((entry, index) => {
          const error = countErrors[entry.key];
          const n = index + 1;
          return (
            <div key={entry.key} className="grid gap-1">
              <div className="grid grid-cols-[7rem_minmax(8rem,1fr)_5rem_5rem_auto] items-center gap-1.5">
                <Select
                  value={entry.subject}
                  onValueChange={(v) => updateCount(entry.key, { subject: v as CounterSubject })}
                >
                  <SelectTrigger
                    size="sm"
                    className="w-full"
                    aria-label={t('debugger.countSubjectLabel', { index: n })}
                  >
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {COUNTER_SUBJECTS.map((s) => (
                      <SelectItem key={s} value={s}>
                        {t(`subjects.${s}`)}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <Select value={entry.outcome} onValueChange={(v) => updateCount(entry.key, { outcome: v })}>
                  <SelectTrigger
                    size="sm"
                    className="w-full font-mono"
                    aria-label={t('debugger.countOutcomeLabel', { index: n })}
                  >
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {OUTCOMES.map((o) => (
                      <SelectItem key={o} value={o} className="font-mono">
                        {o}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <Input
                  value={entry.window}
                  onChange={(e) => updateCount(entry.key, { window: e.target.value })}
                  placeholder="1h"
                  aria-label={t('debugger.countWindowLabel', { index: n })}
                  aria-invalid={error === 'key' || error === 'duplicate'}
                  className="h-8 font-mono"
                />
                <Input
                  value={entry.value}
                  onChange={(e) => updateCount(entry.key, { value: e.target.value })}
                  inputMode="numeric"
                  aria-label={t('debugger.countValueLabel', { index: n })}
                  aria-invalid={error === 'value'}
                  className="h-8 font-mono"
                />
                <Button
                  variant="ghost"
                  size="icon-sm"
                  aria-label={t('debugger.removeCount', { index: n })}
                  onClick={() => onCountsChange(counts.filter((c) => c.key !== entry.key))}
                >
                  <Trash2Icon />
                </Button>
              </div>
              <div className="flex items-center gap-2 pl-1 text-xs">
                <code className="font-mono text-muted-foreground">
                  {counterKey(entry.subject, entry.outcome, entry.window)}
                </code>
                {error && (
                  <span role="alert" className="text-destructive">
                    {t(`debugger.countErrors.${error}`)}
                  </span>
                )}
              </div>
            </div>
          );
        })}
        <div>
          <Button
            variant="outline"
            size="sm"
            disabled={counts.length >= MAX_COUNTS}
            onClick={() =>
              onCountsChange([
                ...counts,
                { key: newKey(), subject: 'identity', outcome: 'captcha', window: '1h', value: '1' },
              ])
            }
          >
            <PlusIcon />
            {t('debugger.addCount')}
          </Button>
        </div>
      </div>

      <div className="grid content-start gap-2">
        <div className="space-y-0.5">
          <h3 className="text-sm font-semibold">{t('debugger.banCountsTitle')}</h3>
          <p className="text-xs text-muted-foreground">{t('debugger.banCountsHint')}</p>
        </div>
        {banCounts.map((entry, index) => {
          const error = banCountErrors[entry.key];
          const n = index + 1;
          return (
            <div key={entry.key} className="grid gap-1">
              <div className="grid grid-cols-[1fr_5rem_auto] items-center gap-1.5">
                <Input
                  value={entry.window}
                  onChange={(e) => updateBan(entry.key, { window: e.target.value })}
                  placeholder="30d"
                  aria-label={t('debugger.banWindowLabel', { index: n })}
                  aria-invalid={error === 'key' || error === 'duplicate'}
                  className="h-8 font-mono"
                />
                <Input
                  value={entry.value}
                  onChange={(e) => updateBan(entry.key, { value: e.target.value })}
                  inputMode="numeric"
                  aria-label={t('debugger.banValueLabel', { index: n })}
                  aria-invalid={error === 'value'}
                  className="h-8 font-mono"
                />
                <Button
                  variant="ghost"
                  size="icon-sm"
                  aria-label={t('debugger.removeBanCount', { index: n })}
                  onClick={() => onBanCountsChange(banCounts.filter((c) => c.key !== entry.key))}
                >
                  <Trash2Icon />
                </Button>
              </div>
              {error && (
                <span role="alert" className="pl-1 text-xs text-destructive">
                  {t(`debugger.countErrors.${error}`)}
                </span>
              )}
            </div>
          );
        })}
        <div>
          <Button
            variant="outline"
            size="sm"
            disabled={banCounts.length >= MAX_BAN_COUNTS}
            onClick={() => onBanCountsChange([...banCounts, { key: newKey(), window: '7d', value: '1' }])}
          >
            <PlusIcon />
            {t('debugger.addBanCount')}
          </Button>
        </div>
      </div>
    </section>
  );
}
