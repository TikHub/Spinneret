import { blockMap, flowMap, scalar, scalarSeq, type YamlEntries, type YamlNode } from '../yaml/emit';
import { type YamlValue } from '../yaml/parse';

/**
 * Shared helpers of the policy form models. Models keep every editable value
 * as a string (the raw input); an empty string means "not set" and the key is
 * omitted from the generated YAML so the server default applies.
 */

export type YamlObject = { [key: string]: YamlValue };

/** Raised when parsed YAML cannot be represented by a form model. */
export class ModelReadError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'ModelReadError';
  }
}

/** Optional `bind` block of a policy (kept as-is by the builders). */
export interface BindModel {
  site: string;
  client: string;
  endpointGroup: string;
}

/** Fields every policy kind shares. */
export interface PolicyMetaModel {
  name: string;
  description: string;
  bind: BindModel | null;
}

let keySequence = 0;

/** Client-side key for list items (React keys); never written to YAML. */
export function newKey(): string {
  keySequence += 1;
  return `k${keySequence}`;
}

function describe(value: YamlValue): string {
  if (value === null) return 'null';
  if (Array.isArray(value)) return 'a list';
  return typeof value === 'object' ? 'a mapping' : `"${String(value)}"`;
}

export function isObject(value: YamlValue | undefined): value is YamlObject {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

/** Reads a mapping; null or missing values become an empty mapping. */
export function asObject(value: YamlValue | undefined, path: string): YamlObject {
  if (value === undefined || value === null) return {};
  if (!isObject(value))
    throw new ModelReadError(`${path || 'document'} must be a mapping (got ${describe(value)})`);
  return value;
}

/** Rejects keys the form cannot represent. */
export function checkKeys(obj: YamlObject, allowed: readonly string[], path: string): void {
  for (const key of Object.keys(obj)) {
    if (!allowed.includes(key)) {
      throw new ModelReadError(`unsupported field ${path ? `${path}.` : ''}${key}`);
    }
  }
}

function scalarText(value: YamlValue | undefined, path: string): string {
  if (value === undefined || value === null) return '';
  if (typeof value === 'object')
    throw new ModelReadError(`${path} must be a scalar (got ${describe(value)})`);
  return String(value);
}

/** Reads a scalar as text ("" when missing). */
export function readText(obj: YamlObject, key: string, path: string): string {
  return scalarText(obj[key], joinPath(path, key));
}

/** Reads a boolean; missing values use the default. */
export function readBool(obj: YamlObject, key: string, path: string, fallback: boolean): boolean {
  const value = obj[key];
  if (value === undefined || value === null) return fallback;
  if (typeof value === 'boolean') return value;
  throw new ModelReadError(`${joinPath(path, key)} must be true or false (got ${describe(value)})`);
}

/** Reads a list of scalars (a single scalar counts as a one-item list). */
export function readStringList(obj: YamlObject, key: string, path: string): string[] {
  const value = obj[key];
  const full = joinPath(path, key);
  if (value === undefined || value === null) return [];
  if (!Array.isArray(value)) return [scalarText(value, full)];
  return value.map((item, i) => {
    const text = scalarText(item, `${full}[${i}]`);
    // The form drops blank entries; keep such YAML in the YAML editor (the server rejects them).
    if (text.trim() === '') throw new ModelReadError(`${full}[${i}] must not be empty`);
    return text;
  });
}

/** Reads a list of mappings. */
export function readObjectList(obj: YamlObject, key: string, path: string): YamlObject[] {
  const value = obj[key];
  const full = joinPath(path, key);
  if (value === undefined || value === null) return [];
  if (!Array.isArray(value)) throw new ModelReadError(`${full} must be a list (got ${describe(value)})`);
  return value.map((item, i) => asObject(item, `${full}[${i}]`));
}

export function joinPath(path: string, key: string): string {
  return path ? `${path}.${key}` : key;
}

export const META_KEYS = ['name', 'description', 'bind'] as const;

export function readMeta(doc: YamlObject): PolicyMetaModel {
  const bindValue = doc.bind;
  let bind: BindModel | null = null;
  if (bindValue !== undefined && bindValue !== null) {
    const b = asObject(bindValue, 'bind');
    checkKeys(b, ['site', 'client', 'endpoint_group'], 'bind');
    bind = {
      site: readText(b, 'site', 'bind'),
      client: readText(b, 'client', 'bind'),
      endpointGroup: readText(b, 'endpoint_group', 'bind'),
    };
  }
  return { name: readText(doc, 'name', ''), description: readText(doc, 'description', ''), bind };
}

/** name, description and bind entries (name is always written). */
export function metaEntries(meta: PolicyMetaModel): YamlEntries {
  return [
    ['name', scalar(meta.name.trim())],
    // Kept verbatim (block scalar descriptions end with a line break).
    ['description', meta.description.trim() === '' ? undefined : scalar(meta.description)],
    [
      'bind',
      meta.bind
        ? flowMap([
            ['site', text(meta.bind.site)],
            ['client', text(meta.bind.client)],
            ['endpoint_group', text(meta.bind.endpointGroup)],
          ])
        : undefined,
    ],
  ];
}

const NUMBER_TEXT = /^[-+]?(\d+(\.\d*)?|\.\d+)([eE][-+]?\d+)?$/;

/** Number field: omitted when empty, a number when numeric, otherwise the raw text (the server reports it). */
export function num(value: string): YamlNode | undefined {
  const trimmed = value.trim();
  if (trimmed === '') return undefined;
  return NUMBER_TEXT.test(trimmed) ? scalar(Number(trimmed)) : scalar(trimmed);
}

/** Text or duration field: omitted when empty. */
export function text(value: string): YamlNode | undefined {
  const trimmed = value.trim();
  return trimmed === '' ? undefined : scalar(trimmed);
}

export function bool(value: boolean): YamlNode {
  return scalar(value);
}

/** Flow list of non-empty strings (always written, [] when empty). */
export function list(values: readonly string[]): YamlNode {
  return scalarSeq(values.map((v) => v.trim()).filter(Boolean));
}

/** Flow list omitted when empty. */
export function optionalList(values: readonly string[]): YamlNode | undefined {
  const cleaned = values.map((v) => v.trim()).filter(Boolean);
  return cleaned.length === 0 ? undefined : scalarSeq(cleaned);
}

/** Block mapping omitted when none of its entries is set. */
export function section(entries: YamlEntries): YamlNode | undefined {
  return entries.some(([, v]) => v !== undefined) ? blockMap(entries) : undefined;
}
