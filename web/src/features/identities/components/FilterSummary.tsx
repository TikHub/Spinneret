import { TriangleAlertIcon } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { Badge } from '@/components/ui/badge';

import { countActiveFilters, type IdentityListParams } from '../identitySearch';

/** Badges describing the active identity filter (or a warning when there is none). */
export function FilterSummary({ params }: { params: IdentityListParams }) {
  const { t } = useTranslation('identities');
  if (countActiveFilters(params) === 0) {
    return (
      <p className="flex items-start gap-2 rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-sm text-amber-700 dark:text-amber-400">
        <TriangleAlertIcon className="mt-0.5 size-4 shrink-0" aria-hidden />
        {t('bulk.noFilters')}
      </p>
    );
  }
  const items: Array<[string, string]> = [];
  if (params.site) items.push([t('fields.site'), params.site]);
  if (params.type) items.push([t('fields.type'), params.type]);
  if (params.states.length > 0) {
    items.push([t('fields.states'), params.states.map((s) => t(`common:states.${s}`)).join(', ')]);
  }
  if (params.tags.length > 0) items.push([t('fields.tags'), params.tags.join(', ')]);
  if (params.accountRef) items.push([t('fields.accountRef'), params.accountRef]);
  if (params.region) items.push([t('fields.region'), params.region]);
  if (params.search) items.push([t('fields.search'), params.search]);
  if (params.minScore !== undefined || params.maxScore !== undefined) {
    items.push([t('fields.score'), `${params.minScore ?? 0} – ${params.maxScore ?? 100}`]);
  }
  if (params.includeRetired) items.push([t('filters.includeRetired'), t('bulk.yes')]);
  return (
    <div className="flex flex-wrap gap-1.5" aria-label={t('bulk.filterSummary')}>
      {items.map(([label, value]) => (
        <Badge key={label} variant="outline" className="max-w-full font-normal">
          <span className="text-muted-foreground">{label}:</span>
          <span className="truncate">{value}</span>
        </Badge>
      ))}
    </div>
  );
}
