import { useCallback, useEffect, useMemo, useState, type DragEvent } from 'react';

import { type URIRule } from '@/gen/spinneret/v1/site_admin_pb';

import {
  draftsFromRules,
  moveRule,
  newRuleDraft,
  removeRule,
  rulesChanged,
  updateRule,
  validateRuleList,
  type RuleDraft,
} from './uriRules';

interface DraftState {
  /** Rules as last loaded or saved. */
  base: RuleDraft[];
  rows: RuleDraft[];
}

/**
 * Editable rule list state: initialized once from the loaded rules, then
 * edited locally until saved or reset. Tracks drag and drop.
 */
export function useRuleDraft(loaded: readonly URIRule[] | undefined) {
  const [state, setState] = useState<DraftState>();
  const [dragKey, setDragKey] = useState<string>();
  const [overKey, setOverKey] = useState<string>();
  const [armedKey, setArmedKey] = useState<string>();

  // Adopt the first loaded rule set during render (later refetches never overwrite edits).
  if (state === undefined && loaded !== undefined) {
    const drafts = draftsFromRules(loaded);
    setState({ base: drafts, rows: drafts });
  }

  // A pressed handle makes its row draggable. Releasing the pointer anywhere without starting a
  // drag disarms it, so text selection inside the row's inputs never drags the row.
  useEffect(() => {
    if (armedKey === undefined || dragKey !== undefined) return undefined;
    // Not pointercancel: browsers fire it when a native drag starts, which must stay armed.
    const disarm = () => setArmedKey(undefined);
    window.addEventListener('pointerup', disarm);
    return () => window.removeEventListener('pointerup', disarm);
  }, [armedKey, dragKey]);

  const rows = useMemo(() => state?.rows ?? [], [state]);
  const hints = useMemo(() => validateRuleList(rows), [rows]);
  const dirty = state !== undefined && rulesChanged(state.base, state.rows);

  const setRows = useCallback(
    (next: (rows: readonly RuleDraft[]) => readonly RuleDraft[]) =>
      setState((prev) => (prev ? { ...prev, rows: [...next(prev.rows)] } : prev)),
    [],
  );

  const focusHandle = (key: string) =>
    requestAnimationFrame(() => {
      // Row keys are generated ("rule-<n>") or rule IDs ("uri_<hex>"): safe inside the selector.
      document.querySelector<HTMLButtonElement>(`[data-rule-handle="${key}"]`)?.focus();
    });

  const actions = {
    add: () => setRows((list) => [...list, newRuleDraft()]),
    change: (key: string, patch: Partial<RuleDraft>) => setRows((list) => updateRule(list, key, patch)),
    remove: (key: string) => setRows((list) => removeRule(list, key)),
    move: (key: string, to: number) => {
      setRows((list) =>
        moveRule(
          list,
          list.findIndex((r) => r.key === key),
          to,
        ),
      );
      focusHandle(key);
    },
    /** Discards local edits. */
    reset: () => setState((prev) => (prev ? { ...prev, rows: prev.base } : prev)),
    /** Replaces base and rows with stored rules (after a save or a reload). */
    saved: (rules: readonly URIRule[]) => {
      const drafts = draftsFromRules(rules);
      setState({ base: drafts, rows: drafts });
    },
  };

  const drag = {
    dragKey,
    overKey,
    armedKey,
    arm: (key: string, armed: boolean) => setArmedKey(armed ? key : undefined),
    start: (key: string, event: DragEvent<HTMLLIElement>) => {
      event.dataTransfer.effectAllowed = 'move';
      event.dataTransfer.setData('text/plain', key);
      setDragKey(key);
    },
    over: (key: string, event: DragEvent<HTMLLIElement>) => {
      if (!dragKey) return;
      event.preventDefault();
      event.dataTransfer.dropEffect = 'move';
      if (overKey !== key) setOverKey(key);
    },
    drop: (key: string, event: DragEvent<HTMLLIElement>) => {
      event.preventDefault();
      if (dragKey && dragKey !== key) {
        setRows((list) =>
          moveRule(
            list,
            list.findIndex((r) => r.key === dragKey),
            list.findIndex((r) => r.key === key),
          ),
        );
      }
      setDragKey(undefined);
      setOverKey(undefined);
      setArmedKey(undefined);
    },
    end: () => {
      setDragKey(undefined);
      setOverKey(undefined);
      setArmedKey(undefined);
    },
  };

  return { ready: state !== undefined, rows, hints, dirty, actions, drag };
}
