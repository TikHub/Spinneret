import { SlidersHorizontalIcon } from 'lucide-react';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';

import { TagsInput } from '@/components/TagsInput';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { FormField } from '@/components/ui/form';
import { Input } from '@/components/ui/input';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';
import { Switch } from '@/components/ui/switch';

import { type IdentityListParams } from '../identitySearch';

type MoreFilters = Pick<
  IdentityListParams,
  'tags' | 'accountRef' | 'region' | 'minScore' | 'maxScore' | 'includeRetired'
>;

interface Draft {
  tags: string[];
  accountRef: string;
  region: string;
  minScore: string;
  maxScore: string;
  includeRetired: boolean;
}

function toDraft(filters: MoreFilters): Draft {
  return {
    tags: [...filters.tags],
    accountRef: filters.accountRef,
    region: filters.region,
    minScore: filters.minScore === undefined ? '' : String(filters.minScore),
    maxScore: filters.maxScore === undefined ? '' : String(filters.maxScore),
    includeRetired: filters.includeRetired,
  };
}

function parseScore(value: string): number | undefined | 'invalid' {
  if (value.trim() === '') return undefined;
  const n = Number(value);
  return Number.isFinite(n) && n >= 0 && n <= 100 ? n : 'invalid';
}

/** Count of active secondary filters. */
function activeCount(filters: MoreFilters): number {
  return (
    (filters.tags.length > 0 ? 1 : 0) +
    (filters.accountRef ? 1 : 0) +
    (filters.region ? 1 : 0) +
    (filters.minScore !== undefined || filters.maxScore !== undefined ? 1 : 0) +
    (filters.includeRetired ? 1 : 0)
  );
}

export interface MoreFiltersPopoverProps {
  value: MoreFilters;
  onApply: (filters: MoreFilters) => void;
}

/** Popover with tags, account, region, score range and retired filters (applied together). */
export function MoreFiltersPopover({ value, onApply }: MoreFiltersPopoverProps) {
  const { t } = useTranslation('identities');
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState<Draft>(() => toDraft(value));
  const count = activeCount(value);

  const minScore = parseScore(draft.minScore);
  const maxScore = parseScore(draft.maxScore);
  const rangeError =
    minScore === 'invalid' || maxScore === 'invalid'
      ? t('filters.scoreInvalid')
      : minScore !== undefined && maxScore !== undefined && minScore > maxScore
        ? t('filters.scoreOrder')
        : undefined;

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (rangeError || minScore === 'invalid' || maxScore === 'invalid') return;
    onApply({
      tags: draft.tags,
      accountRef: draft.accountRef.trim(),
      region: draft.region.trim(),
      minScore,
      maxScore,
      includeRetired: draft.includeRetired,
    });
    setOpen(false);
  };

  return (
    <Popover
      open={open}
      onOpenChange={(next) => {
        if (next) setDraft(toDraft(value));
        setOpen(next);
      }}
    >
      <PopoverTrigger asChild>
        <Button variant="outline" size="sm" className={count > 0 ? 'border-primary/40' : undefined}>
          <SlidersHorizontalIcon />
          {t('filters.more')}
          {count > 0 && (
            <Badge variant="secondary" className="px-1.5 py-0">
              {count}
            </Badge>
          )}
        </Button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-80">
        <form className="grid gap-3" onSubmit={submit}>
          <FormField label={t('fields.tags')} description={t('filters.tagsHint')}>
            <TagsInput
              value={draft.tags}
              onChange={(tags) => setDraft({ ...draft, tags })}
              maxTags={32}
              validate={(tag) => tag.length <= 64}
            />
          </FormField>
          <FormField label={t('fields.accountRef')}>
            <Input
              value={draft.accountRef}
              maxLength={256}
              onChange={(e) => setDraft({ ...draft, accountRef: e.target.value })}
            />
          </FormField>
          <FormField label={t('fields.region')}>
            <Input
              value={draft.region}
              maxLength={64}
              onChange={(e) => setDraft({ ...draft, region: e.target.value })}
            />
          </FormField>
          <div className="grid grid-cols-2 gap-2">
            <FormField label={t('filters.minScore')} error={rangeError}>
              <Input
                type="number"
                min={0}
                max={100}
                step="any"
                inputMode="decimal"
                value={draft.minScore}
                onChange={(e) => setDraft({ ...draft, minScore: e.target.value })}
              />
            </FormField>
            <FormField label={t('filters.maxScore')}>
              <Input
                type="number"
                min={0}
                max={100}
                step="any"
                inputMode="decimal"
                value={draft.maxScore}
                onChange={(e) => setDraft({ ...draft, maxScore: e.target.value })}
              />
            </FormField>
          </div>
          <FormField inline label={t('filters.includeRetired')}>
            <Switch
              checked={draft.includeRetired}
              onCheckedChange={(includeRetired) => setDraft({ ...draft, includeRetired })}
            />
          </FormField>
          <div className="flex justify-end gap-2">
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={() =>
                setDraft({
                  tags: [],
                  accountRef: '',
                  region: '',
                  minScore: '',
                  maxScore: '',
                  includeRetired: false,
                })
              }
            >
              {t('common:actions.clear')}
            </Button>
            <Button type="submit" size="sm" disabled={Boolean(rangeError)}>
              {t('common:actions.apply')}
            </Button>
          </div>
        </form>
      </PopoverContent>
    </Popover>
  );
}
