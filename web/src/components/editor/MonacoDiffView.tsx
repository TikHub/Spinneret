import { DiffEditor, type DiffOnMount } from '@monaco-editor/react';
import { useEffect, useLayoutEffect, useRef } from 'react';

import { useTheme } from '@/app/theme/ThemeProvider';
import { cn } from '@/lib/utils';

import './monaco';
import { type DiffViewProps } from './types';

type DiffEditorInstance = Parameters<DiffOnMount>[0];
type ContentListener = { dispose: () => void };

/** Monaco implementation of DiffView (loaded lazily). */
export default function MonacoDiffView({
  original,
  modified,
  language = 'yaml',
  height = 420,
  sideBySide = true,
  className,
  editable = false,
  onModifiedChange,
}: DiffViewProps) {
  const { resolvedTheme } = useTheme();
  // The latest callback is read on every change, so a new (e.g. inline) handler
  // passed after mount is honoured instead of the one captured at mount time.
  const onChangeRef = useRef(onModifiedChange);
  useLayoutEffect(() => {
    onChangeRef.current = onModifiedChange;
  });
  const listenerRef = useRef<ContentListener | undefined>(undefined);
  const editorRef = useRef<DiffEditorInstance | undefined>(undefined);

  // @monaco-editor/react disposes the text models before the diff editor, which makes
  // Monaco report "TextModel got disposed before DiffEditorWidget model got reset" as an
  // uncaught error. This cleanup runs before the library's (parent effects first), so
  // detach the models from the widget and dispose them here; the library then only
  // disposes the editor.
  useEffect(
    () => () => {
      listenerRef.current?.dispose();
      const editor = editorRef.current;
      if (!editor) return;
      const models = editor.getModel();
      editor.setModel(null);
      models?.original.dispose();
      models?.modified.dispose();
    },
    [],
  );

  const handleMount: DiffOnMount = (editor) => {
    editorRef.current = editor;
    listenerRef.current?.dispose();
    const modifiedEditor = editor.getModifiedEditor();
    listenerRef.current = modifiedEditor.onDidChangeModelContent(() =>
      onChangeRef.current?.(modifiedEditor.getValue()),
    );
  };

  return (
    <div className={cn('overflow-hidden rounded-md border', className)} style={{ height }}>
      <DiffEditor
        original={original}
        modified={modified}
        language={language}
        theme={resolvedTheme === 'dark' ? 'vs-dark' : 'vs'}
        onMount={handleMount}
        options={{
          renderSideBySide: sideBySide,
          readOnly: !editable,
          originalEditable: false,
          minimap: { enabled: false },
          fontSize: 13,
          scrollBeyondLastLine: false,
          automaticLayout: true,
          renderOverviewRuler: false,
        }}
      />
    </div>
  );
}
