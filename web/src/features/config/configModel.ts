import { type EditorLanguage, type EditorMarker } from '@/components/editor/CodeEditor';
import { type ConfigItemInfo } from '@/gen/spinneret/v1/config_admin_pb';

/** Content formats of config items (CreateConfigItemRequest.format). */
export const CONFIG_FORMATS = ['json', 'yaml', 'text'] as const;
export type ConfigFormat = (typeof CONFIG_FORMATS)[number];

/** Creatable group names (no reserved "_" prefix). */
export const CONFIG_GROUP_PATTERN = /^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$/;
/** Config keys. */
export const CONFIG_KEY_PATTERN = /^[a-zA-Z0-9][a-zA-Z0-9_./-]{0,127}$/;
/** Groups starting with this prefix are system-maintained and read-only. */
export const SYSTEM_GROUP_PREFIX = '_';

export const MAX_CONFIG_DESCRIPTION_LENGTH = 1024;
export const MAX_CONFIG_COMMENT_LENGTH = 1024;
export const MAX_CONFIG_CONTENT_BYTES = 4 * 1024 * 1024;
export const MAX_CONFIG_SCHEMA_BYTES = 1024 * 1024;

export type ConfigNameError = 'required' | 'reserved' | 'pattern';

/** Validates a group name for a new config item. */
export function validateConfigGroup(group: string): ConfigNameError | undefined {
  if (group === '') return 'required';
  if (group.startsWith(SYSTEM_GROUP_PREFIX)) return 'reserved';
  return CONFIG_GROUP_PATTERN.test(group) ? undefined : 'pattern';
}

/** Validates a config key. */
export function validateConfigKey(key: string): ConfigNameError | undefined {
  if (key === '') return 'required';
  return CONFIG_KEY_PATTERN.test(key) ? undefined : 'pattern';
}

export function isConfigFormat(value: string): value is ConfigFormat {
  return (CONFIG_FORMATS as readonly string[]).includes(value);
}

/** Reports whether a group is system-maintained (e.g. "_runtime"). */
export function isSystemGroup(group: string): boolean {
  return group.startsWith(SYSTEM_GROUP_PREFIX);
}

/**
 * Reports whether an item is read-only in the console: system groups, and
 * virtual items without an ID (the server exposes "_runtime" items that way).
 */
export function isReadOnlyItem(item: Pick<ConfigItemInfo, 'group' | 'id'>): boolean {
  return isSystemGroup(item.group) || item.id === '';
}

/** Editor language for a content format. */
export function formatLanguage(format: string): EditorLanguage {
  if (format === 'json') return 'json';
  if (format === 'yaml') return 'yaml';
  return 'plaintext';
}

/** Whether a JSON Schema can be attached to items of the format. */
export function formatSupportsSchema(format: string): boolean {
  return format === 'json' || format === 'yaml';
}

/** UTF-8 size of a string in bytes. */
export function utf8ByteLength(value: string): number {
  return new TextEncoder().encode(value).length;
}

/** 1-based line and column of a string offset. */
export function offsetToPosition(text: string, offset: number): { line: number; column: number } {
  const clamped = Math.max(0, Math.min(offset, text.length));
  let line = 1;
  let lineStart = 0;
  for (let i = 0; i < clamped; i++) {
    if (text.charCodeAt(i) === 10) {
      line++;
      lineStart = i + 1;
    }
  }
  return { line, column: clamped - lineStart + 1 };
}

/** Extracts a position from engine-specific JSON.parse error messages. */
function jsonErrorPosition(text: string, message: string): { line: number; column: number } {
  const lineColumn = /line (\d+) column (\d+)/i.exec(message);
  if (lineColumn) return { line: Number(lineColumn[1]), column: Number(lineColumn[2]) };
  const position = /position (\d+)/i.exec(message);
  if (position) return offsetToPosition(text, Number(position[1]));
  // Unexpected end of input: point at the end of the text.
  if (/end of (json )?input|unexpected end/i.test(message)) return offsetToPosition(text, text.length);
  return { line: 1, column: 1 };
}

/** Result of a JSON syntax check. */
export type JsonCheck =
  | { ok: true; value: unknown }
  | { ok: false; /** True when the text is blank. */ empty: boolean; marker: EditorMarker };

/** Parses JSON and reports the first syntax error as an editor marker. */
export function checkJsonSyntax(text: string): JsonCheck {
  if (text.trim() === '') {
    return { ok: false, empty: true, marker: { line: 1, column: 1, message: 'empty', severity: 'error' } };
  }
  try {
    return { ok: true, value: JSON.parse(text) as unknown };
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    const { line, column } = jsonErrorPosition(text, message);
    return { ok: false, empty: false, marker: { line, column, message, severity: 'error' } };
  }
}

export type SchemaError = 'syntax' | 'type' | 'size';

/**
 * Checks a JSON Schema document: empty means "no schema"; otherwise it must be
 * valid JSON whose top level is an object or a boolean (JSON Schema 2020-12).
 */
export function validateSchemaDocument(schema: string): SchemaError | undefined {
  if (schema.trim() === '') return undefined;
  if (utf8ByteLength(schema) > MAX_CONFIG_SCHEMA_BYTES) return 'size';
  const parsed = checkJsonSyntax(schema);
  if (!parsed.ok) return 'syntax';
  const { value } = parsed;
  const isObject = typeof value === 'object' && value !== null && !Array.isArray(value);
  return isObject || typeof value === 'boolean' ? undefined : 'type';
}

/** Human label of an item: "group/key". */
export function itemLabel(item: Pick<ConfigItemInfo, 'group' | 'key'>): string {
  return `${item.group}/${item.key}`;
}

/** A group of the config tree. */
export interface ConfigTreeGroup {
  group: string;
  system: boolean;
  items: ConfigItemInfo[];
}

/** Groups items by group name: system groups last, then by name; keys sorted inside a group. */
export function buildConfigTree(items: readonly ConfigItemInfo[]): ConfigTreeGroup[] {
  const groups = new Map<string, ConfigItemInfo[]>();
  for (const item of items) {
    const list = groups.get(item.group);
    if (list) list.push(item);
    else groups.set(item.group, [item]);
  }
  return [...groups.entries()]
    .map(([group, list]) => ({
      group,
      system: isSystemGroup(group),
      items: [...list].sort((a, b) => a.key.localeCompare(b.key)),
    }))
    .sort((a, b) => {
      if (a.system !== b.system) return a.system ? 1 : -1;
      return a.group.localeCompare(b.group);
    });
}
