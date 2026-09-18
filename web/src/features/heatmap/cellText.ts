import { type Translate } from '@/lib/errors';
import { humanizeDuration } from '@/lib/duration';
import { formatNumber } from '@/lib/format';

import {
  type HeatmapCellView,
  type HeatmapColumnView,
  type HeatmapMetric,
  type HeatmapRowView,
} from './heatmapModel';
import { type HeatmapTooltipLine } from './heatmapOption';

/** Short text of a cell for the table view. */
export function cellText(cell: HeatmapCellView, metric: HeatmapMetric, t: Translate, lng?: string): string {
  if (metric === 'score') {
    return cell.score === undefined
      ? t('cell.baseline')
      : formatNumber(cell.score, { maximumFractionDigits: 0 }, lng);
  }
  if (cell.permanent) return t('common:duration.permanent');
  if (cell.cooldownMs > 0) return humanizeDuration(cell.cooldownMs, { t });
  return cell.available ? t('cell.available') : t('cell.unavailable');
}

/** Tooltip lines of a chart cell. */
export function tooltipLines(
  cell: HeatmapCellView,
  row: HeatmapRowView,
  column: HeatmapColumnView,
  t: Translate,
  lng?: string,
): HeatmapTooltipLine[] {
  return [
    { label: t('tooltip.identity'), value: row.identityId },
    { label: t('tooltip.label'), value: row.label },
    {
      label: t('tooltip.state'),
      value: t(`common:states.${row.state || 'unknown'}`, { defaultValue: row.state }),
    },
    { label: t('tooltip.group'), value: column.name },
    {
      label: t('tooltip.score'),
      value:
        cell.score === undefined
          ? t('cell.baseline')
          : formatNumber(cell.score, { maximumFractionDigits: 1 }, lng),
    },
    {
      label: t('tooltip.cooldown'),
      value: cell.permanent
        ? t('tooltip.permanentBan')
        : cell.cooldownMs > 0
          ? humanizeDuration(cell.cooldownMs, { t })
          : t('tooltip.noCooldown'),
    },
    { label: t('tooltip.available'), value: cell.available ? t('tooltip.yes') : t('tooltip.no') },
    ...(cell.hasHotState ? [] : [{ label: t('tooltip.hotState'), value: t('tooltip.noHotState') }]),
  ];
}
