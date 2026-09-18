import { lazy, Suspense } from 'react';
import { useTranslation } from 'react-i18next';

import { Skeleton } from '@/components/ui/skeleton';

import { type CodeEditorProps } from './types';

const MonacoCodeEditor = lazy(() => import('./MonacoCodeEditor'));

export type { CodeEditorProps, EditorLanguage, EditorMarker } from './types';

/**
 * Monaco-based YAML/JSON/plain text editor with validation markers. Monaco is
 * loaded on first render (separate chunk); a skeleton is shown meanwhile.
 */
export function CodeEditor(props: CodeEditorProps) {
  const { t } = useTranslation();
  const height = props.height ?? 360;
  return (
    <Suspense
      fallback={
        <Skeleton
          className="rounded-md border"
          style={{ height }}
          role="status"
          aria-label={t('editor.loading')}
        />
      }
    >
      <MonacoCodeEditor {...props} />
    </Suspense>
  );
}
