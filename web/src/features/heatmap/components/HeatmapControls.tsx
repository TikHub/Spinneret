import { useTranslation } from 'react-i18next';

import { StateBadge } from '@/components/StateBadge';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs';
import { MultiSelectMenu } from '@/features/requests/shared/MultiSelectMenu';
import { SiteSelect } from '@/features/requests/shared/SiteSelect';

import { HEATMAP_LIMITS, HEATMAP_METRICS, HEATMAP_STATES, type HeatmapMetric } from '../heatmapModel';
import { HEATMAP_VIEWS, normalizeStates, type HeatmapSelection, type HeatmapView } from '../heatmapSearch';

const STATE_OPTIONS = HEATMAP_STATES.map((state) => ({
  value: state,
  label: <StateBadge state={state} kind="identity" />,
}));

export interface HeatmapControlsProps {
  selection: HeatmapSelection;
  /** Effective site and client (the URL may omit them). */
  site: string;
  client: string;
  clients: readonly string[];
  onChange: (selection: HeatmapSelection) => void;
}

/** Site, client, metric, state filter, row limit and view toggles of the heatmap. */
export function HeatmapControls({ selection, site, client, clients, onChange }: HeatmapControlsProps) {
  const { t } = useTranslation('heatmap');
  const set = (patch: Partial<HeatmapSelection>) => onChange({ ...selection, ...patch });

  return (
    <div className="flex flex-wrap items-center gap-2" role="toolbar" aria-label={t('controls')}>
      <SiteSelect value={site} onChange={(next) => set({ site: next, client: '' })} />
      <Select
        value={client}
        onValueChange={(next) => set({ site, client: next })}
        disabled={clients.length === 0}
      >
        <SelectTrigger size="sm" className="w-32" aria-label={t('client')}>
          <SelectValue placeholder={t('client')} />
        </SelectTrigger>
        <SelectContent>
          {clients.map((c) => (
            <SelectItem key={c} value={c}>
              {c}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <Tabs value={selection.metric} onValueChange={(v) => set({ metric: v as HeatmapMetric })}>
        <TabsList className="h-8" aria-label={t('metric')}>
          {HEATMAP_METRICS.map((metric) => (
            <TabsTrigger key={metric} value={metric} className="text-xs">
              {t(`metrics.${metric}`)}
            </TabsTrigger>
          ))}
        </TabsList>
      </Tabs>
      <MultiSelectMenu
        label={t('states')}
        emptyText={t('allStates')}
        options={STATE_OPTIONS}
        value={selection.states}
        onChange={(states) => set({ states: normalizeStates(states) })}
      />
      <Select value={String(selection.limit)} onValueChange={(v) => set({ limit: Number(v) })}>
        <SelectTrigger size="sm" className="w-36" aria-label={t('limit')}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {HEATMAP_LIMITS.map((limit) => (
            <SelectItem key={limit} value={String(limit)}>
              {t('limitOption', { count: limit })}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <Tabs value={selection.view} onValueChange={(v) => set({ view: v as HeatmapView })} className="ml-auto">
        <TabsList className="h-8" aria-label={t('view')}>
          {HEATMAP_VIEWS.map((view) => (
            <TabsTrigger key={view} value={view} className="text-xs">
              {t(`views.${view}`)}
            </TabsTrigger>
          ))}
        </TabsList>
      </Tabs>
    </div>
  );
}
