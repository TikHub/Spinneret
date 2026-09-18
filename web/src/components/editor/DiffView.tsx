import { lazy, Suspense } from 'react';
import { useTranslation } from 'react-i18next';

import { Skeleton } from '@/components/ui/skeleton';

import { type DiffViewProps } from './types';

const MonacoDiffView = lazy(() => import('./MonacoDiffView'));

export type { DiffViewProps } from './types';

/** Monaco diff editor for version comparisons (lazy-loaded). */
export function DiffView(props: DiffViewProps) {
  const { t } = useTranslation();
  const height = props.height ?? 420;
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
      <MonacoDiffView {...props} />
    </Suspense>
  );
}
