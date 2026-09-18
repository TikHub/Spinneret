import { useTranslation } from 'react-i18next';

import { Input } from '@/components/ui/input';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';

import { SCOPE_DEFINITIONS, type TokenScopeName } from '../../scopes';

/**
 * Select value standing for "every site" (Radix Select items cannot use "").
 * Site names may contain underscores but never "*", so this cannot collide.
 */
const ALL_SITES = '*';

export interface ScopeArgInputProps {
  name: TokenScopeName;
  value: string;
  onChange: (value: string) => void;
  /** Active namespace name (used in the secret path example). */
  namespace: string;
  /** Site names for site-scoped scopes; undefined while loading or unavailable. */
  siteNames: readonly string[] | undefined;
  /** True when site names could not be loaded (falls back to free text). */
  sitesUnavailable: boolean;
  invalid: boolean;
  describedBy?: string;
  label: string;
}

/** Argument control of one scope row: site select, group glob, path glob or nothing. */
export function ScopeArgInput({
  name,
  value,
  onChange,
  namespace,
  siteNames,
  sitesUnavailable,
  invalid,
  describedBy,
  label,
}: ScopeArgInputProps) {
  const { t } = useTranslation('access');
  const kind = SCOPE_DEFINITIONS[name].arg;

  if (kind === 'none') {
    return (
      <p className="flex h-9 items-center text-sm text-muted-foreground" aria-label={label}>
        {t('scopes.builder.noArgument')}
      </p>
    );
  }

  if (kind === 'site' && !sitesUnavailable) {
    const options = siteNames ?? [];
    // Keep a value that is not (or not yet) in the list selectable.
    const withCurrent = value !== '' && !options.includes(value) ? [value, ...options] : options;
    return (
      <Select
        value={value === '' ? ALL_SITES : value}
        onValueChange={(next) => onChange(next === ALL_SITES ? '' : next)}
        disabled={siteNames === undefined}
      >
        <SelectTrigger
          className="w-full"
          aria-label={label}
          aria-invalid={invalid}
          aria-describedby={describedBy}
        >
          <SelectValue placeholder={t('scopes.builder.loadingSites')} />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={ALL_SITES}>{t('scopes.builder.allSites')}</SelectItem>
          {withCurrent.map((site) => (
            <SelectItem key={site} value={site} className="font-mono">
              {site}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    );
  }

  const placeholder =
    kind === 'site'
      ? t('scopes.builder.sitePlaceholder')
      : kind === 'group'
        ? t('scopes.builder.groupPlaceholder')
        : t('scopes.builder.pathPlaceholder', { namespace: namespace || 'prod' });

  return (
    <Input
      value={value}
      onChange={(event) => onChange(event.target.value)}
      placeholder={placeholder}
      spellCheck={false}
      autoComplete="off"
      className="font-mono"
      aria-label={label}
      aria-invalid={invalid}
      aria-describedby={describedBy}
    />
  );
}
