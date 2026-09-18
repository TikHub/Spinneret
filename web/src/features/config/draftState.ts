import { type ConfigItemInfo } from '@/gen/spinneret/v1/config_admin_pb';

import { formatLanguage } from './configModel';

/** Editable fields of a config item. */
export interface ConfigDraftValues {
  content: string;
  schema: string;
  description: string;
}

/** Local editor state: the server baseline the edits started from, and the edited values. */
export interface ConfigEditorState {
  /** Item identity ("namespace/group/key"); a different item resets the state. */
  identity: string;
  baseline: ConfigDraftValues;
  values: ConfigDraftValues;
}

export const EMPTY_DRAFT_VALUES: ConfigDraftValues = { content: '', schema: '', description: '' };

/** Identity of an item across refetches. */
export function itemIdentity(item: Pick<ConfigItemInfo, 'namespace' | 'group' | 'key'> | undefined): string {
  return item ? `${item.namespace}/${item.group}/${item.key}` : '';
}

/** Values the editor starts from: the draft when there is one, otherwise the published content. */
export function baselineOf(item: ConfigItemInfo | undefined): ConfigDraftValues {
  if (!item) return EMPTY_DRAFT_VALUES;
  return {
    content: item.hasDraft ? item.draftContent : item.publishedContent,
    schema: item.schemaJson,
    description: item.description,
  };
}

export function sameValues(a: ConfigDraftValues, b: ConfigDraftValues): boolean {
  return a.content === b.content && a.schema === b.schema && a.description === b.description;
}

export function initialEditorState(item: ConfigItemInfo | undefined): ConfigEditorState {
  const baseline = baselineOf(item);
  return { identity: itemIdentity(item), baseline, values: baseline };
}

export function isDirty(state: ConfigEditorState): boolean {
  return !sameValues(state.values, state.baseline);
}

/**
 * Reconciles local state with the latest server item: another item resets
 * the state; server changes are adopted while there are no local edits and
 * kept aside (see hasRemoteChanges) otherwise.
 */
export function reconcileEditorState(
  state: ConfigEditorState,
  item: ConfigItemInfo | undefined,
): ConfigEditorState {
  const identity = itemIdentity(item);
  if (state.identity !== identity) return initialEditorState(item);
  const server = baselineOf(item);
  if (!isDirty(state) && !sameValues(state.baseline, server)) {
    return { identity, baseline: server, values: server };
  }
  return state;
}

/** True when the server item changed after the local edits started. */
export function hasRemoteChanges(state: ConfigEditorState, item: ConfigItemInfo | undefined): boolean {
  return (
    isDirty(state) && state.identity === itemIdentity(item) && !sameValues(state.baseline, baselineOf(item))
  );
}

/**
 * State after a successful save: the saved item becomes the baseline; edits
 * made while the request was running are kept.
 */
export function applySaved(
  state: ConfigEditorState,
  sent: ConfigDraftValues,
  saved: ConfigItemInfo,
): ConfigEditorState {
  const baseline = baselineOf(saved);
  const values = sameValues(state.values, sent) ? baseline : state.values;
  return { identity: itemIdentity(saved), baseline, values };
}

/** SaveConfigDraft fields: optional fields are sent only when they changed. */
export function draftRequest(
  id: string,
  values: ConfigDraftValues,
  item: Pick<ConfigItemInfo, 'schemaJson' | 'description'>,
): { id: string; content: string; schemaJson?: string; description?: string } {
  return {
    id,
    content: values.content,
    ...(values.schema !== item.schemaJson ? { schemaJson: values.schema } : {}),
    ...(values.description !== item.description ? { description: values.description } : {}),
  };
}

/** Line-level change counts of a diff. */
export interface LineStats {
  added: number;
  removed: number;
}

function splitLines(text: string): string[] {
  if (text === '') return [];
  return text.replace(/\r\n/g, '\n').split('\n');
}

/**
 * Counts added and removed lines with a longest common subsequence (bounded
 * inputs); large inputs fall back to multiset counting.
 */
export function lineStats(original: string, modified: string): LineStats {
  const a = splitLines(original);
  const b = splitLines(modified);
  const MAX_CELLS = 4_000_000;
  if (a.length * b.length > MAX_CELLS) {
    const counts = new Map<string, number>();
    for (const line of a) counts.set(line, (counts.get(line) ?? 0) + 1);
    let common = 0;
    for (const line of b) {
      const n = counts.get(line) ?? 0;
      if (n > 0) {
        common++;
        counts.set(line, n - 1);
      }
    }
    return { added: b.length - common, removed: a.length - common };
  }
  let previous = new Array<number>(b.length + 1).fill(0);
  for (let i = 1; i <= a.length; i++) {
    const current = new Array<number>(b.length + 1).fill(0);
    for (let j = 1; j <= b.length; j++) {
      current[j] =
        a[i - 1] === b[j - 1] ? (previous[j - 1] ?? 0) + 1 : Math.max(previous[j] ?? 0, current[j - 1] ?? 0);
    }
    previous = current;
  }
  const common = previous[b.length] ?? 0;
  return { added: b.length - common, removed: a.length - common };
}

/** Everything the publish confirmation needs. */
export interface PublishDiff {
  original: string;
  modified: string;
  language: ReturnType<typeof formatLanguage>;
  /** True when the draft differs from the published content. */
  changed: boolean;
  /** True when the item was never published. */
  firstPublish: boolean;
  /** True when unsaved local edits are saved as the draft before publishing. */
  savesDraftFirst: boolean;
  /** expected_version for PublishConfig (optimistic concurrency). */
  expectedVersion: number;
  nextVersion: number;
  stats: LineStats;
}

/**
 * Prepares the publish confirmation: the published content against the draft
 * that will be published (local edits when dirty, the stored draft otherwise).
 */
export function preparePublishDiff(item: ConfigItemInfo, state: ConfigEditorState): PublishDiff {
  const dirty = isDirty(state);
  const draft = dirty ? state.values.content : item.hasDraft ? item.draftContent : item.publishedContent;
  const firstPublish = item.currentVersion === 0;
  const hasDraft = dirty || item.hasDraft;
  const changed = hasDraft && (firstPublish || draft !== item.publishedContent);
  return {
    original: item.publishedContent,
    modified: draft,
    language: formatLanguage(item.format),
    changed,
    firstPublish,
    savesDraftFirst: dirty,
    expectedVersion: item.currentVersion,
    nextVersion: item.currentVersion + 1,
    stats: lineStats(item.publishedContent, draft),
  };
}
