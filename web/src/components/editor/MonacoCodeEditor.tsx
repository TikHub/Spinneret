import Editor, { type OnMount } from '@monaco-editor/react';
import { useEffect, useState } from 'react';

import { useTheme } from '@/app/theme/ThemeProvider';
import { cn } from '@/lib/utils';

import { monaco } from './monaco';
import { type CodeEditorProps, type EditorMarker } from './types';

type StandaloneEditor = Parameters<OnMount>[0];

const MARKER_OWNER = 'spinneret';

function toSeverity(severity: EditorMarker['severity']): monaco.MarkerSeverity {
  switch (severity) {
    case 'warning':
      return monaco.MarkerSeverity.Warning;
    case 'info':
      return monaco.MarkerSeverity.Info;
    default:
      return monaco.MarkerSeverity.Error;
  }
}

const BASE_EDITOR_OPTIONS = {
  minimap: { enabled: false },
  fontSize: 13,
  lineNumbersMinChars: 3,
  scrollBeyondLastLine: false,
  tabSize: 2,
  insertSpaces: true,
  automaticLayout: true,
  renderWhitespace: 'boundary',
  wordWrap: 'on',
  fixedOverflowWidgets: true,
} as const;

/** Monaco implementation of CodeEditor (loaded lazily). */
export default function MonacoCodeEditor({
  value,
  onChange,
  language = 'yaml',
  readOnly = false,
  height = 360,
  markers,
  path,
  className,
  options,
  'aria-label': ariaLabel,
}: CodeEditorProps) {
  const { resolvedTheme } = useTheme();
  const [editor, setEditor] = useState<StandaloneEditor>();

  useEffect(() => {
    const model = editor?.getModel();
    if (!model) return;
    const lineCount = model.getLineCount();
    monaco.editor.setModelMarkers(
      model,
      MARKER_OWNER,
      (markers ?? []).map((m) => {
        const startLine = Math.min(Math.max(1, m.line), lineCount);
        const endLine = Math.min(Math.max(startLine, m.endLine ?? startLine), lineCount);
        return {
          startLineNumber: startLine,
          startColumn: m.column ?? 1,
          endLineNumber: endLine,
          endColumn: m.endColumn ?? model.getLineMaxColumn(endLine),
          message: m.message,
          severity: toSeverity(m.severity),
        };
      }),
    );
  }, [editor, markers, value]);

  return (
    <div className={cn('overflow-hidden rounded-md border', className)} style={{ height }}>
      <Editor
        value={value}
        language={language}
        path={path}
        theme={resolvedTheme === 'dark' ? 'vs-dark' : 'vs'}
        onChange={(next) => onChange?.(next ?? '')}
        onMount={(instance) => setEditor(instance)}
        options={{ ...BASE_EDITOR_OPTIONS, readOnly, ariaLabel, ...options }}
      />
    </div>
  );
}
