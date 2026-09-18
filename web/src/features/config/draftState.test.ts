import { create, type MessageInitShape } from '@bufbuild/protobuf';
import { describe, expect, it } from 'vitest';

import { ConfigItemInfoSchema, type ConfigItemInfo } from '@/gen/spinneret/v1/config_admin_pb';

import {
  applySaved,
  baselineOf,
  draftRequest,
  hasRemoteChanges,
  initialEditorState,
  isDirty,
  lineStats,
  preparePublishDiff,
  reconcileEditorState,
} from './draftState';

function item(patch: MessageInitShape<typeof ConfigItemInfoSchema> = {}): ConfigItemInfo {
  return create(ConfigItemInfoSchema, {
    id: 'cfg_1',
    namespace: 'prod',
    group: 'crawler',
    key: 'search.json',
    format: 'json',
    currentVersion: 3,
    publishedContent: '{"a":1}',
    ...patch,
  });
}

describe('editor state', () => {
  it('starts from the draft when there is one', () => {
    expect(baselineOf(item()).content).toBe('{"a":1}');
    expect(baselineOf(item({ hasDraft: true, draftContent: '{"a":2}' })).content).toBe('{"a":2}');
  });

  it('adopts server changes while clean and keeps local edits when dirty', () => {
    const state = initialEditorState(item());
    const updated = item({ publishedContent: '{"a":9}', currentVersion: 4 });
    expect(reconcileEditorState(state, updated).values.content).toBe('{"a":9}');

    const dirty = { ...state, values: { ...state.values, content: '{"mine":1}' } };
    const kept = reconcileEditorState(dirty, updated);
    expect(kept).toBe(dirty);
    expect(hasRemoteChanges(kept, updated)).toBe(true);
  });

  it('resets when another item is selected', () => {
    const state = { ...initialEditorState(item()), values: { content: 'x', schema: '', description: '' } };
    const other = item({ key: 'other.json', publishedContent: '[]' });
    const next = reconcileEditorState(state, other);
    expect(next.values.content).toBe('[]');
    expect(isDirty(next)).toBe(false);
  });

  it('marks saved values clean and keeps edits made during the save', () => {
    const state = {
      ...initialEditorState(item()),
      values: { content: '{"a":2}', schema: '', description: '' },
    };
    const sent = state.values;
    const saved = item({ hasDraft: true, draftContent: '{"a":2}' });
    expect(isDirty(applySaved(state, sent, saved))).toBe(false);

    const typedMore = { ...state, values: { ...state.values, content: '{"a":3}' } };
    const after = applySaved(typedMore, sent, saved);
    expect(isDirty(after)).toBe(true);
    expect(after.values.content).toBe('{"a":3}');
  });

  it('sends optional draft fields only when they changed', () => {
    const base = item({ schemaJson: '{}', description: 'd' });
    expect(draftRequest('cfg_1', { content: 'c', schema: '{}', description: 'd' }, base)).toEqual({
      id: 'cfg_1',
      content: 'c',
    });
    expect(draftRequest('cfg_1', { content: 'c', schema: '', description: 'new' }, base)).toEqual({
      id: 'cfg_1',
      content: 'c',
      schemaJson: '',
      description: 'new',
    });
  });
});

describe('preparePublishDiff', () => {
  it('diffs the published content against the stored draft', () => {
    const it0 = item({ hasDraft: true, draftContent: '{"a":2}' });
    const diff = preparePublishDiff(it0, initialEditorState(it0));
    expect(diff).toMatchObject({
      original: '{"a":1}',
      modified: '{"a":2}',
      language: 'json',
      changed: true,
      firstPublish: false,
      savesDraftFirst: false,
      expectedVersion: 3,
      nextVersion: 4,
      stats: { added: 1, removed: 1 },
    });
  });

  it('uses unsaved local edits and saves them first', () => {
    const it0 = item();
    const state = { ...initialEditorState(it0), values: { content: '{"b":1}', schema: '', description: '' } };
    const diff = preparePublishDiff(it0, state);
    expect(diff.modified).toBe('{"b":1}');
    expect(diff.savesDraftFirst).toBe(true);
    expect(diff.changed).toBe(true);
  });

  it('reports nothing to publish without a draft or with identical content', () => {
    const clean = item();
    expect(preparePublishDiff(clean, initialEditorState(clean)).changed).toBe(false);
    const same = item({ hasDraft: true, draftContent: '{"a":1}' });
    expect(preparePublishDiff(same, initialEditorState(same)).changed).toBe(false);
  });

  it('allows a first publish of a draft', () => {
    const fresh = item({ currentVersion: 0, publishedContent: '', hasDraft: true, draftContent: 'x: 1' });
    const diff = preparePublishDiff(fresh, initialEditorState(fresh));
    expect(diff).toMatchObject({ firstPublish: true, changed: true, expectedVersion: 0, nextVersion: 1 });
  });
});

describe('lineStats', () => {
  it('counts added and removed lines', () => {
    expect(lineStats('a\nb\nc', 'a\nc\nd\ne')).toEqual({ added: 2, removed: 1 });
    expect(lineStats('', 'a\nb')).toEqual({ added: 2, removed: 0 });
    expect(lineStats('same', 'same')).toEqual({ added: 0, removed: 0 });
  });
});
