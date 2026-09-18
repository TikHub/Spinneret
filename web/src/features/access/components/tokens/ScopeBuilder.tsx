import { PlusIcon, SparklesIcon, Trash2Icon } from 'lucide-react';
import { useId } from 'react';
import { useTranslation } from 'react-i18next';

import { Button } from '@/components/ui/button';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { SimpleTooltip } from '@/components/ui/tooltip';

import { toKeySegment } from '../../forms';
import {
  applyPreset,
  createScopeRow,
  expandPreset,
  MAX_SCOPES,
  SCOPE_DEFINITIONS,
  SCOPE_PRESETS,
  scopesFromRows,
  TOKEN_SCOPE_NAMES,
  validateScopeRows,
  type ScopeRow,
  type TokenScopeName,
} from '../../scopes';
import { useSiteNames } from '../../useSiteNames';

import { ScopeArgInput } from './ScopeArgInput';
import { ScopeChips } from './ScopeChips';

export interface ScopeBuilderProps {
  rows: readonly ScopeRow[];
  onChange: (rows: ScopeRow[]) => void;
  /** Namespace the token is bound to (site names come from it). */
  namespace: string;
  /** Show row validation errors (after the first submit attempt). */
  showErrors: boolean;
}

/** Builds token scope strings row by row, with presets and a live preview. */
export function ScopeBuilder({ rows, onChange, namespace, showErrors }: ScopeBuilderProps) {
  const { t } = useTranslation('access');
  const baseId = useId();
  const needsSites = rows.some((row) => SCOPE_DEFINITIONS[row.name].arg === 'site');
  const sites = useSiteNames(namespace, needsSites);
  const errors = validateScopeRows(rows);
  const scopes = scopesFromRows(rows);

  const update = (id: string, patch: Partial<Omit<ScopeRow, 'id'>>) =>
    onChange(rows.map((row) => (row.id === id ? { ...row, ...patch } : row)));

  const addRow = () => {
    const used = new Set(rows.map((row) => row.name));
    const next = TOKEN_SCOPE_NAMES.find((name) => !used.has(name)) ?? 'lease:acquire';
    onChange([...rows, createScopeRow(next)]);
  };

  return (
    <div className="grid gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <span className="flex items-center gap-1 text-xs text-muted-foreground">
          <SparklesIcon className="size-3.5" aria-hidden />
          {t('scopes.presets.label')}
        </span>
        {SCOPE_PRESETS.map((preset) => (
          <SimpleTooltip key={preset.id} content={expandPreset(preset.id).join(', ')}>
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="h-7"
              onClick={() => onChange(applyPreset(rows, preset.id))}
            >
              {t(`scopes.presets.${preset.id}`)}
            </Button>
          </SimpleTooltip>
        ))}
      </div>

      {rows.length === 0 ? (
        <p className="rounded-md border border-dashed px-3 py-4 text-center text-sm text-muted-foreground">
          {t('scopes.builder.empty')}
        </p>
      ) : (
        <ul className="grid gap-2" aria-label={t('scopes.builder.listLabel')}>
          {rows.map((row, index) => {
            const error = showErrors ? errors[row.id] : undefined;
            const hintId = `${baseId}-${row.id}-hint`;
            return (
              <li key={row.id} className="grid gap-1 rounded-md border bg-muted/20 p-2">
                <div className="grid grid-cols-[minmax(9rem,11rem)_1fr_auto] items-center gap-2">
                  <Select
                    value={row.name}
                    onValueChange={(name) => update(row.id, { name: name as TokenScopeName, arg: '' })}
                  >
                    <SelectTrigger
                      className="w-full font-mono"
                      aria-label={t('scopes.builder.scopeLabel', { index: index + 1 })}
                    >
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {TOKEN_SCOPE_NAMES.map((name) => (
                        <SelectItem key={name} value={name} className="font-mono">
                          {name}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  <ScopeArgInput
                    name={row.name}
                    value={row.arg}
                    onChange={(arg) => update(row.id, { arg })}
                    namespace={namespace}
                    siteNames={sites.data}
                    sitesUnavailable={sites.isError}
                    invalid={error !== undefined}
                    describedBy={hintId}
                    label={t('scopes.builder.argLabel', { index: index + 1 })}
                  />
                  <SimpleTooltip content={t('scopes.builder.remove')}>
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon-sm"
                      aria-label={t('scopes.builder.removeRow', { index: index + 1 })}
                      onClick={() => onChange(rows.filter((r) => r.id !== row.id))}
                    >
                      <Trash2Icon />
                    </Button>
                  </SimpleTooltip>
                </div>
                <p
                  id={hintId}
                  className={error ? 'text-xs text-destructive' : 'text-xs text-muted-foreground'}
                >
                  {error
                    ? t(`scopes.errors.${error}`, { namespace: namespace || 'prod' })
                    : t(`scopes.names.${toKeySegment(row.name)}.hint`, { namespace: namespace || 'prod' })}
                </p>
              </li>
            );
          })}
        </ul>
      )}

      <div className="flex flex-wrap items-center justify-between gap-2">
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={addRow}
          disabled={rows.length >= MAX_SCOPES}
        >
          <PlusIcon />
          {t('scopes.builder.add')}
        </Button>
        {sites.isError && needsSites && (
          <p className="text-xs text-muted-foreground">{t('scopes.builder.sitesUnavailable')}</p>
        )}
      </div>

      {scopes.length > 0 && (
        <div className="grid gap-1">
          <span className="text-xs text-muted-foreground">{t('scopes.builder.preview')}</span>
          <ScopeChips scopes={scopes} max={MAX_SCOPES} />
        </div>
      )}
    </div>
  );
}
