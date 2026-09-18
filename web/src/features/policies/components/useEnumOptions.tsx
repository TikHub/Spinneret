import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';

import { type SelectOption } from './fields';

/**
 * Select options for wire values: the value in monospace with its translated
 * description (`<prefix>.<value>` in the policies namespace).
 */
export function useEnumOptions(values: readonly string[], prefix: string, withCode = true): SelectOption[] {
  const { t } = useTranslation('policies');
  return useMemo(
    () =>
      values.map((value) => ({
        value,
        label: withCode ? (
          <span className="inline-flex items-baseline gap-2">
            <span className="font-mono text-xs">{value}</span>
            <span className="text-muted-foreground">{t(`${prefix}.${value}`, { defaultValue: '' })}</span>
          </span>
        ) : (
          t(`${prefix}.${value}`, { defaultValue: value })
        ),
      })),
    [values, prefix, withCode, t],
  );
}
