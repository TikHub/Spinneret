import { type EditorMarker } from '@/components/editor/CodeEditor';
import { isValidSecretPath } from '@/features/secrets/secretPath';

import { offsetToPosition } from './configModel';

/**
 * Secret reference syntax in config content: ${secret:<path>} or
 * ${secret:<path>#<version>} (mirrors internal/configcenter/secretref.go).
 * The console never resolves references; nodes receive resolved content.
 */
export const SECRET_REF_PREFIX = '${secret:';
const MAX_SECRET_PATH = 256;
const MAX_VERSION_DIGITS = 10;
const MAX_REF_LENGTH = SECRET_REF_PREFIX.length + MAX_SECRET_PATH + 1 + MAX_VERSION_DIGITS + 1;
const MAX_INT32 = 2 ** 31 - 1;

export interface SecretRef {
  path: string;
  /** 0 = current version. */
  version: number;
}

export type SecretRefIssue = 'unterminated' | 'path' | 'version';

export interface SecretRefProblem {
  issue: SecretRefIssue;
  line: number;
  column: number;
}

export interface SecretRefScan {
  /** Distinct references in order of first appearance. */
  refs: SecretRef[];
  /** Malformed references (the server rejects content containing any). */
  problems: SecretRefProblem[];
}

function parseBody(body: string): SecretRef | SecretRefIssue {
  const hash = body.indexOf('#');
  const path = hash < 0 ? body : body.slice(0, hash);
  if (!isValidSecretPath(path)) return 'path';
  if (hash < 0) return { path, version: 0 };
  const version = body.slice(hash + 1);
  if (!/^[1-9][0-9]{0,9}$/.test(version) || version.length > MAX_VERSION_DIGITS) return 'version';
  const n = Number(version);
  return n > MAX_INT32 ? 'version' : { path, version: n };
}

/** Finds every secret reference of the content and reports malformed ones. */
export function scanSecretRefs(content: string): SecretRefScan {
  const refs: SecretRef[] = [];
  const seen = new Set<string>();
  const problems: SecretRefProblem[] = [];
  let offset = 0;
  for (;;) {
    const start = content.indexOf(SECRET_REF_PREFIX, offset);
    if (start < 0) break;
    const bodyStart = start + SECRET_REF_PREFIX.length;
    const window = content.slice(bodyStart, Math.min(content.length, start + MAX_REF_LENGTH));
    const closing = window.indexOf('}');
    if (closing < 0) {
      problems.push({ issue: 'unterminated', ...offsetToPosition(content, start) });
      offset = bodyStart;
      continue;
    }
    const parsed = parseBody(window.slice(0, closing));
    if (typeof parsed === 'string') {
      problems.push({ issue: parsed, ...offsetToPosition(content, start) });
    } else {
      const id = formatSecretRef(parsed);
      if (!seen.has(id)) {
        seen.add(id);
        refs.push(parsed);
      }
    }
    offset = bodyStart + closing + 1;
  }
  return { refs, problems };
}

/** "<path>" or "<path>#<version>". */
export function formatSecretRef(ref: SecretRef): string {
  return ref.version > 0 ? `${ref.path}#${ref.version}` : ref.path;
}

/** Editor markers for malformed references with translated messages. */
export function secretRefMarkers(
  problems: readonly SecretRefProblem[],
  message: (issue: SecretRefIssue) => string,
): EditorMarker[] {
  return problems.map((p) => ({
    line: p.line,
    column: p.column,
    endLine: p.line,
    endColumn: p.column + SECRET_REF_PREFIX.length,
    message: message(p.issue),
    severity: 'error' as const,
  }));
}
