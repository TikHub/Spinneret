import { byteLength, parseMaxConcurrency, PROXY_LIMITS, validateSessionTemplate } from './proxyForm';

/** Import formats of ImportProxiesRequest.format. */
export const IMPORT_FORMATS = ['lines', 'jsonl', 'csv'] as const;
export type ImportFormat = (typeof IMPORT_FORMATS)[number];

/** Maximum import payload (ImportProxiesRequest.data max_bytes). */
export const MAX_IMPORT_BYTES = 32 * 1024 * 1024;

/** Import dialog defaults (ProxyDefaults); an empty kind means "datacenter". */
export interface ImportDefaultsForm {
  kind: string;
  region: string;
  city: string;
  provider: string;
  tags: string[];
  /** Empty = server default (1). */
  maxConcurrency: string;
  sessionTemplate: string;
}

export const EMPTY_IMPORT_DEFAULTS: ImportDefaultsForm = {
  kind: '',
  region: '',
  city: '',
  provider: '',
  tags: [],
  maxConcurrency: '',
  sessionTemplate: '',
};

/** Guesses the format from a file name; undefined when unknown. */
export function formatFromFileName(name: string): ImportFormat | undefined {
  const lower = name.toLowerCase();
  if (lower.endsWith('.jsonl') || lower.endsWith('.ndjson') || lower.endsWith('.json')) return 'jsonl';
  if (lower.endsWith('.csv')) return 'csv';
  if (lower.endsWith('.txt') || lower.endsWith('.lst') || lower.endsWith('.list')) return 'lines';
  return undefined;
}

/** Counts non-blank data lines (for a preview before the dry run). */
export function countDataLines(data: string, format: ImportFormat): number {
  const lines = data.split(/\r?\n/).filter((line) => line.trim() !== '');
  if (format === 'lines') return lines.filter((line) => !line.trim().startsWith('#')).length;
  if (format === 'csv') return Math.max(0, lines.length - 1);
  return lines.length;
}

export type ImportIssue =
  | 'data_required'
  | 'data_too_large'
  | 'region_too_long'
  | 'city_too_long'
  | 'provider_too_long'
  | 'max_concurrency'
  | 'session_template';

/**
 * Size, content hash and line count of the import data. Computing them walks
 * the whole payload (up to 32 MiB), so callers memoize the summary per data
 * string instead of recomputing it on every render.
 */
export interface ImportDataSummary {
  blank: boolean;
  bytes: number;
  hash: string;
  length: number;
}

export function summarizeImportData(data: string): ImportDataSummary {
  return { blank: !/\S/.test(data), bytes: byteLength(data), hash: hashString(data), length: data.length };
}

/** Validates the import inputs before sending them. */
export function validateImport(data: ImportDataSummary, defaults: ImportDefaultsForm): ImportIssue[] {
  const issues: ImportIssue[] = [];
  if (data.blank) issues.push('data_required');
  else if (data.bytes > MAX_IMPORT_BYTES) issues.push('data_too_large');
  if (byteLength(defaults.region.trim()) > PROXY_LIMITS.region) issues.push('region_too_long');
  if (byteLength(defaults.city.trim()) > PROXY_LIMITS.city) issues.push('city_too_long');
  if (byteLength(defaults.provider.trim()) > PROXY_LIMITS.provider) issues.push('provider_too_long');
  if (defaults.maxConcurrency.trim() !== '' && parseMaxConcurrency(defaults.maxConcurrency) === undefined) {
    issues.push('max_concurrency');
  }
  if (validateSessionTemplate(defaults.sessionTemplate.trim())) issues.push('session_template');
  return issues;
}

/** Plain init object of ProxyDefaults. */
export interface ProxyDefaultsInit {
  kind: string;
  region: string;
  city: string;
  provider: string;
  tags: string[];
  maxConcurrency: number;
  sessionTemplate: string;
}

export function buildImportDefaults(defaults: ImportDefaultsForm): ProxyDefaultsInit {
  return {
    kind: defaults.kind,
    region: defaults.region.trim(),
    city: defaults.city.trim(),
    provider: defaults.provider.trim(),
    tags: [...defaults.tags],
    maxConcurrency: parseMaxConcurrency(defaults.maxConcurrency) ?? 0,
    sessionTemplate: defaults.sessionTemplate.trim(),
  };
}

/**
 * Fingerprint of the import inputs. The real import is only offered after a
 * dry run of exactly these inputs.
 */
export function importFingerprint(
  format: ImportFormat,
  data: ImportDataSummary,
  defaults: ImportDefaultsForm,
): string {
  return JSON.stringify([format, data.length, data.hash, buildImportDefaults(defaults)]);
}

/** Fast non-cryptographic 32-bit FNV-1a hash (change detection only). */
export function hashString(value: string): string {
  let hash = 0x811c9dc5;
  for (let i = 0; i < value.length; i += 1) {
    hash ^= value.charCodeAt(i);
    hash = Math.imul(hash, 0x01000193);
  }
  return (hash >>> 0).toString(16);
}
