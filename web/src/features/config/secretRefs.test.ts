import { describe, expect, it } from 'vitest';

import { formatSecretRef, scanSecretRefs, secretRefMarkers } from './secretRefs';

describe('scanSecretRefs', () => {
  it('finds distinct references with optional versions', () => {
    const content =
      'a: ${secret:signing/api_key}\nb: ${secret:signing/api_key#3}\nc: ${secret:signing/api_key}';
    const scan = scanSecretRefs(content);
    expect(scan.problems).toEqual([]);
    expect(scan.refs).toEqual([
      { path: 'signing/api_key', version: 0 },
      { path: 'signing/api_key', version: 3 },
    ]);
    expect(scan.refs.map(formatSecretRef)).toEqual(['signing/api_key', 'signing/api_key#3']);
  });

  it('reports malformed references with positions', () => {
    const content = 'x\n  ${secret:Bad/Path}\n${secret:ok#0}\n${secret:a/../b}\n${secret:never-closed';
    const scan = scanSecretRefs(content);
    expect(scan.refs).toEqual([]);
    expect(scan.problems).toEqual([
      { issue: 'path', line: 2, column: 3 },
      { issue: 'version', line: 3, column: 1 },
      { issue: 'path', line: 4, column: 1 },
      { issue: 'unterminated', line: 5, column: 1 },
    ]);
  });

  it('builds editor markers', () => {
    const markers = secretRefMarkers([{ issue: 'path', line: 2, column: 3 }], (issue) => `bad ${issue}`);
    expect(markers).toEqual([
      { line: 2, column: 3, endLine: 2, endColumn: 12, message: 'bad path', severity: 'error' },
    ]);
  });

  it('ignores content without references', () => {
    expect(scanSecretRefs('{"secret": "plain"}')).toEqual({ refs: [], problems: [] });
  });
});
