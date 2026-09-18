import { useEffect, useState } from 'react';

import { useRegisterUnsavedChanges } from '@/app/unsaved/useUnsavedChanges';

import { type UnsavedDrafts } from './components/editor/PolicyEditor';

/**
 * Unsaved editor text per policy, kept while switching policies and cleared
 * when `scope` (tenant/namespace) changes. Editors mutate the map directly, so
 * the checks below read it when asked instead of on render: page unloads warn
 * and tenant or namespace switches ask while any policy has a draft.
 */
export function useUnsavedPolicyDrafts(scope: string): UnsavedDrafts {
  const [unsaved, setUnsaved] = useState<UnsavedDrafts>(() => new Map());
  const [lastScope, setLastScope] = useState(scope);
  if (lastScope !== scope) {
    setLastScope(scope);
    setUnsaved(new Map());
  }

  useRegisterUnsavedChanges(() => unsaved.size > 0);

  useEffect(() => {
    const warn = (event: BeforeUnloadEvent) => {
      if (unsaved.size > 0) event.preventDefault();
    };
    window.addEventListener('beforeunload', warn);
    return () => window.removeEventListener('beforeunload', warn);
  }, [unsaved]);

  return unsaved;
}
