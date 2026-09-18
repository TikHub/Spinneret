const formatterCache = new Map<string, Intl.NumberFormat>();

function numberFormat(locale: string | undefined, options: Intl.NumberFormatOptions): Intl.NumberFormat {
  const key = `${locale ?? ''}|${JSON.stringify(options)}`;
  let fmt = formatterCache.get(key);
  if (!fmt) {
    fmt = new Intl.NumberFormat(locale, options);
    formatterCache.set(key, fmt);
  }
  return fmt;
}

/** Converts protobuf int64 (bigint) or number values to number; undefined becomes 0. */
export function toNumber(value: number | bigint | undefined | null): number {
  if (value === undefined || value === null) return 0;
  return typeof value === 'bigint' ? Number(value) : value;
}

/** Grouped integer or decimal, e.g. "12,345" or "3.14". */
export function formatNumber(
  value: number | bigint | undefined | null,
  options: Intl.NumberFormatOptions = { maximumFractionDigits: 2 },
  locale?: string,
): string {
  if (value === undefined || value === null) return '—';
  if (typeof value === 'number' && !Number.isFinite(value)) return '—';
  return numberFormat(locale, options).format(value);
}

/** Compact notation, e.g. "1.2K", "3.4M". */
export function formatCompact(value: number | bigint | undefined | null, locale?: string): string {
  return formatNumber(value, { notation: 'compact', maximumFractionDigits: 1 }, locale);
}

/** Ratio 0..1 as a percentage, e.g. 0.1234 -> "12.3%". */
export function formatPercent(ratio: number | undefined | null, fractionDigits = 1, locale?: string): string {
  if (ratio === undefined || ratio === null || !Number.isFinite(ratio)) return '—';
  return numberFormat(locale, {
    style: 'percent',
    minimumFractionDigits: 0,
    maximumFractionDigits: fractionDigits,
  }).format(ratio);
}

/** Per-second rate, e.g. "12.5/s". */
export function formatRate(perSecond: number | undefined | null, locale?: string): string {
  if (perSecond === undefined || perSecond === null || !Number.isFinite(perSecond)) return '—';
  const digits = perSecond >= 100 ? 0 : perSecond >= 10 ? 1 : 2;
  return `${formatNumber(perSecond, { maximumFractionDigits: digits }, locale)}/s`;
}

const BYTE_UNITS = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB'] as const;

/** Binary byte size, e.g. 1536 -> "1.5 KiB". */
export function formatBytes(bytes: number | bigint | undefined | null, fractionDigits = 1): string {
  if (bytes === undefined || bytes === null) return '—';
  let value = toNumber(bytes);
  if (!Number.isFinite(value) || value < 0) return '—';
  let unit = 0;
  while (value >= 1024 && unit < BYTE_UNITS.length - 1) {
    value /= 1024;
    unit += 1;
  }
  const digits = unit === 0 ? 0 : fractionDigits;
  return `${value.toFixed(digits)} ${BYTE_UNITS[unit]}`;
}

/** Latency in milliseconds, e.g. "850 ms" or "1.25 s". */
export function formatLatency(ms: number | bigint | undefined | null): string {
  if (ms === undefined || ms === null) return '—';
  const value = toNumber(ms);
  if (!Number.isFinite(value)) return '—';
  if (value < 1000) return `${Math.round(value)} ms`;
  return `${(value / 1000).toFixed(2)} s`;
}
