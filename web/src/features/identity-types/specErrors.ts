import type { EditorMarker } from '@/components/editor/types';

const LINE_PATTERN = /\bline (\d+)(?:,? column (\d+))?/i;

/**
 * Splits one message at "; " separators outside double-quoted values. The
 * server joins every spec problem with "; " and quotes user values (Go %q), so
 * a separator inside a quoted value must not split.
 */
function splitProblems(message: string): string[] {
  const parts: string[] = [];
  let current = '';
  let quoted = false;
  for (let i = 0; i < message.length; i += 1) {
    const ch = message[i];
    if (quoted && ch === '\\' && i + 1 < message.length) {
      current += ch + message[i + 1];
      i += 1;
      continue;
    }
    if (ch === '"') quoted = !quoted;
    if (!quoted && ch === ';' && message[i + 1] === ' ') {
      parts.push(current);
      current = '';
      i += 1;
      continue;
    }
    current += ch;
  }
  parts.push(current);
  return parts;
}

/** Splits server validation messages into individual problems ("a; line 3: b" -> ["a", "line 3: b"]). */
export function splitSpecErrors(messages: readonly string[]): string[] {
  const out: string[] = [];
  for (const message of messages) {
    for (const line of message.split(/\r?\n/)) {
      for (const part of splitProblems(line)) {
        const trimmed = part.trim();
        if (trimmed && !out.includes(trimmed)) out.push(trimmed);
      }
    }
  }
  return out;
}

/** Editor markers for problems that mention a YAML line (and optionally a column). */
export function specErrorMarkers(messages: readonly string[], lineCount?: number): EditorMarker[] {
  const markers: EditorMarker[] = [];
  for (const problem of splitSpecErrors(messages)) {
    const match = LINE_PATTERN.exec(problem);
    if (!match) continue;
    let line = Number(match[1]);
    if (!Number.isInteger(line) || line < 1) continue;
    if (lineCount !== undefined && lineCount > 0) line = Math.min(line, lineCount);
    const column = match[2] ? Number(match[2]) : undefined;
    markers.push({ line, column, message: problem, severity: 'error' });
  }
  return markers;
}
