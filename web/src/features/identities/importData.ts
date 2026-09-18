import { type ImportIdentitiesResponse } from '@/gen/spinneret/v1/identity_admin_pb';

/** ImportIdentitiesRequest.data limit (32 MiB). */
export const IMPORT_MAX_BYTES = 33_554_432;
/** Server row limit per call. */
export const IMPORT_MAX_ROWS = 50_000;
/** Failures rendered in the result table; the rest are summarized. */
export const IMPORT_FAILURES_SHOWN = 500;

export const IMPORT_FORMATS = ['jsonl', 'csv'] as const;
export type ImportFormat = (typeof IMPORT_FORMATS)[number];
export const IMPORT_MODES = ['upsert', 'create_only'] as const;
export type ImportMode = (typeof IMPORT_MODES)[number];

/** Guesses the format from a file name (.csv, .jsonl, .ndjson, .json). */
export function detectImportFormat(fileName: string): ImportFormat | undefined {
  const lower = fileName.toLowerCase();
  if (lower.endsWith('.csv')) return 'csv';
  if (lower.endsWith('.jsonl') || lower.endsWith('.ndjson') || lower.endsWith('.json')) return 'jsonl';
  return undefined;
}

/** UTF-8 size of a string in bytes. */
export function utf8ByteLength(text: string): number {
  let bytes = 0;
  for (let i = 0; i < text.length; i += 1) {
    const code = text.charCodeAt(i);
    if (code < 0x80) bytes += 1;
    else if (code < 0x800) bytes += 2;
    else if (code >= 0xd800 && code <= 0xdbff && i + 1 < text.length) {
      const next = text.charCodeAt(i + 1);
      if (next >= 0xdc00 && next <= 0xdfff) {
        bytes += 4;
        i += 1;
      } else {
        bytes += 3;
      }
    } else bytes += 3;
  }
  return bytes;
}

/** Number of data rows: non-empty lines, minus the header for CSV. */
export function countImportRows(text: string, format: ImportFormat): number {
  if (text.trim() === '') return 0;
  const lines = text.split(/\r?\n/).filter((line) => line.trim() !== '').length;
  return format === 'csv' ? Math.max(0, lines - 1) : lines;
}

export type ImportDataError = 'empty' | 'tooLarge' | 'tooManyRows';

export function validateImportData(text: string, format: ImportFormat): ImportDataError | undefined {
  if (text.trim() === '') return 'empty';
  if (utf8ByteLength(text) > IMPORT_MAX_BYTES) return 'tooLarge';
  if (countImportRows(text, format) > IMPORT_MAX_ROWS) return 'tooManyRows';
  return undefined;
}

/** Inputs that must match between a dry run and the real import. */
export interface ImportInputKey {
  site: string;
  type: string;
  format: ImportFormat;
  mode: ImportMode;
  /** Incremented whenever the data changes. */
  dataVersion: number;
}

export function sameImportInput(a: ImportInputKey | undefined, b: ImportInputKey): boolean {
  return (
    a !== undefined &&
    a.site === b.site &&
    a.type === b.type &&
    a.format === b.format &&
    a.mode === b.mode &&
    a.dataVersion === b.dataVersion
  );
}

export interface ImportSummary {
  created: number;
  updated: number;
  unchanged: number;
  failed: number;
  /** Rows processed (accepted + rejected). */
  total: number;
  /** Failures sorted by line, capped at IMPORT_FAILURES_SHOWN. */
  shownFailures: Array<{ line: number; message: string }>;
  hiddenFailures: number;
}

export function summarizeImport(
  result: Pick<ImportIdentitiesResponse, 'created' | 'updated' | 'unchanged' | 'failed'>,
): ImportSummary {
  const sorted = [...result.failed].sort((a, b) => a.line - b.line);
  const shown = sorted.slice(0, IMPORT_FAILURES_SHOWN).map((f) => ({ line: f.line, message: f.message }));
  return {
    created: result.created,
    updated: result.updated,
    unchanged: result.unchanged,
    failed: result.failed.length,
    total: result.created + result.updated + result.unchanged + result.failed.length,
    shownFailures: shown,
    hiddenFailures: sorted.length - shown.length,
  };
}
