import { resolvePlain } from './parse';

/**
 * Minimal YAML emitter for policy builders. Documents are described as an
 * ordered node tree so key order and flow/block style are deterministic.
 */

export type YamlScalar = string | number | boolean | null;

export type YamlNode =
  | { type: 'scalar'; value: YamlScalar }
  | { type: 'map'; entries: ReadonlyArray<readonly [string, YamlNode]>; flow: boolean }
  | { type: 'seq'; items: readonly YamlNode[]; flow: boolean };

/** Entry list that may contain skipped (undefined) values. */
export type YamlEntries = ReadonlyArray<readonly [string, YamlNode | undefined]>;

export function scalar(value: YamlScalar): YamlNode {
  return { type: 'scalar', value };
}

function compact(entries: YamlEntries): Array<readonly [string, YamlNode]> {
  return entries.filter((e): e is readonly [string, YamlNode] => e[1] !== undefined);
}

/** Block mapping; entries with undefined values are skipped. */
export function blockMap(entries: YamlEntries): YamlNode {
  return { type: 'map', entries: compact(entries), flow: false };
}

/** Flow mapping ({ a: 1, b: x }); entries with undefined values are skipped. */
export function flowMap(entries: YamlEntries): YamlNode {
  return { type: 'map', entries: compact(entries), flow: true };
}

export function blockSeq(items: readonly YamlNode[]): YamlNode {
  return { type: 'seq', items, flow: false };
}

export function flowSeq(items: readonly YamlNode[]): YamlNode {
  return { type: 'seq', items, flow: true };
}

/** Flow sequence of scalars. */
export function scalarSeq(values: readonly YamlScalar[]): YamlNode {
  return flowSeq(values.map(scalar));
}

const RESERVED_PLAIN =
  /^(null|Null|NULL|~|true|True|TRUE|false|False|FALSE|yes|Yes|YES|no|No|NO|on|On|ON|off|Off|OFF|y|Y|n|N)$/;
// First character: no YAML indicator (- ? : , [ ] { } # & * ! | > ' " % @ `); later ones: no flow indicators.
const SAFE_PLAIN = /^[A-Za-z0-9_/.$^(][A-Za-z0-9_ ./:@$^()+\-*?|=]*$/;

// Plain scalars the server's YAML library (go-yaml v3) resolves to numbers or timestamps
// ("_" separators ignored; leading zeros and 0x/0o/0b prefixes are integers).
const V3_NUMBER =
  /^[-+]?(0x[0-9a-fA-F]+|0o[0-7]+|0b[01]+|[0-9]+|(\.[0-9]+|[0-9]+(\.[0-9]*)?)([eE][-+]?[0-9]+)?)$/;
const V3_TIMESTAMP = /^[0-9]{4}-[0-9]{1,2}-[0-9]{1,2}([Tt\s]|$)/;

function isNonStringForServer(value: string): boolean {
  if (!/^[-+0-9.]/.test(value)) return false;
  return V3_NUMBER.test(value.replace(/_/g, '')) || V3_TIMESTAMP.test(value);
}

/** Reports whether a string can be written without quotes and read back unchanged. */
export function isPlainSafe(value: string): boolean {
  if (value === '' || value !== value.trim()) return false;
  if (RESERVED_PLAIN.test(value) || typeof resolvePlain(value) !== 'string') return false;
  if (isNonStringForServer(value)) return false;
  if (!SAFE_PLAIN.test(value)) return false;
  // ": " and " #" change the meaning of a plain scalar; a trailing ":" too.
  return !value.includes(': ') && !value.includes(' #') && !value.endsWith(':');
}

function hasControlChars(value: string): boolean {
  for (let i = 0; i < value.length; i++) {
    const code = value.charCodeAt(i);
    if (code < 0x20 || code === 0x7f) return true;
  }
  return false;
}

/** Formats a string scalar: plain when safe, single-quoted when possible, otherwise double-quoted. */
export function formatString(value: string): string {
  if (isPlainSafe(value)) return value;
  // Control characters (newlines, tabs) need escapes, which only double quotes support.
  if (hasControlChars(value)) return JSON.stringify(value);
  return `'${value.replace(/'/g, "''")}'`;
}

export function formatScalar(value: YamlScalar): string {
  if (value === null) return 'null';
  if (typeof value === 'boolean') return value ? 'true' : 'false';
  if (typeof value === 'number')
    return Number.isFinite(value) ? String(value) : JSON.stringify(String(value));
  return formatString(value);
}

function formatKey(key: string): string {
  return isPlainSafe(key) ? key : JSON.stringify(key);
}

function formatFlow(node: YamlNode): string {
  switch (node.type) {
    case 'scalar':
      return formatScalar(node.value);
    case 'seq':
      return `[${node.items.map(formatFlow).join(', ')}]`;
    case 'map':
      return node.entries.length === 0
        ? '{}'
        : `{ ${node.entries.map(([k, v]) => `${formatKey(k)}: ${formatFlow(v)}`).join(', ')} }`;
  }
}

/** True when the node is written on the same line as its key. */
function isInline(node: YamlNode): boolean {
  if (node.type === 'scalar' || node.flow) return true;
  return node.type === 'map' ? node.entries.length === 0 : node.items.length === 0;
}

function pad(indent: number): string {
  return ' '.repeat(indent);
}

function emitMapLines(
  entries: ReadonlyArray<readonly [string, YamlNode]>,
  indent: number,
  out: string[],
): void {
  for (const [key, value] of entries) {
    if (isInline(value)) {
      out.push(`${pad(indent)}${formatKey(key)}: ${formatFlow(value)}`);
    } else {
      out.push(`${pad(indent)}${formatKey(key)}:`);
      emitBlock(value, indent + 2, out);
    }
  }
}

function emitSeqLines(items: readonly YamlNode[], indent: number, out: string[]): void {
  for (const item of items) {
    if (isInline(item)) {
      out.push(`${pad(indent)}- ${formatFlow(item)}`);
      continue;
    }
    // Block item: its first line shares the "- " marker, the rest is indented under it.
    const nested: string[] = [];
    emitBlock(item, indent + 2, nested);
    const [first = '', ...rest] = nested;
    out.push(`${pad(indent)}- ${first.slice(indent + 2)}`);
    out.push(...rest);
  }
}

function emitBlock(node: YamlNode, indent: number, out: string[]): void {
  if (node.type === 'map') emitMapLines(node.entries, indent, out);
  else if (node.type === 'seq') emitSeqLines(node.items, indent, out);
  else out.push(`${pad(indent)}${formatFlow(node)}`);
}

/** Serializes a document (usually a block mapping) to YAML text ending with a newline. */
export function emitYaml(node: YamlNode): string {
  if (isInline(node)) return `${formatFlow(node)}\n`;
  const out: string[] = [];
  emitBlock(node, 0, out);
  return `${out.join('\n')}\n`;
}

/** Single-line flow representation of a node (used for rule summaries). */
export function flowText(node: YamlNode): string {
  return formatFlow(node);
}
