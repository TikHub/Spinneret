import { create } from '@bufbuild/protobuf';
import { Code, ConnectError } from '@connectrpc/connect';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import {
  ConfigItemInfoSchema,
  type PublishConfigRequest,
  type SaveConfigDraftRequest,
} from '@/gen/spinneret/v1/config_admin_pb';

import type * as DraftState from '../draftState';
import { initialEditorState } from '../draftState';

const prepared = vi.hoisted(() => ({ count: 0 }));

// Count diff preparations (an LCS over lines) to check they only run while the dialog is open.
vi.mock('../draftState', async (importOriginal) => {
  const actual: typeof DraftState = await importOriginal();
  return {
    ...actual,
    preparePublishDiff: (...args: Parameters<typeof actual.preparePublishDiff>) => {
      prepared.count++;
      return actual.preparePublishDiff(...args);
    },
  };
});

const calls = vi.hoisted(() => ({
  save: [] as SaveConfigDraftRequest[],
  publish: [] as PublishConfigRequest[],
  conflict: false,
}));

vi.mock('@/lib/clients', async () => {
  const { createClient, createRouterTransport } = await import('@connectrpc/connect');
  const { ConfigAdminService } = await import('@/gen/spinneret/v1/config_admin_pb');
  const transport = createRouterTransport(({ service }) => {
    service(ConfigAdminService, {
      saveConfigDraft: (req) => {
        calls.save.push(req);
        return { item: { id: req.id, hasDraft: true, draftContent: req.content, currentVersion: 3 } };
      },
      publishConfig: (req) => {
        calls.publish.push(req);
        if (calls.conflict) {
          throw new ConnectError('version changed', Code.FailedPrecondition);
        }
        return { item: { id: req.id, currentVersion: 4 }, version: 4 };
      },
    });
  });
  return { configAdminClient: createClient(ConfigAdminService, transport) };
});

// Monaco cannot run in jsdom; render both sides as text.
vi.mock('@/components/editor/DiffView', () => ({
  DiffView: ({ original, modified }: { original: string; modified: string }) => (
    <div>
      <pre data-testid="diff-original">{original}</pre>
      <pre data-testid="diff-modified">{modified}</pre>
    </div>
  ),
}));

const { PublishConfigDialog } = await import('./PublishConfigDialog');

const item = create(ConfigItemInfoSchema, {
  id: 'cfg_1',
  namespace: 'prod',
  group: 'crawler',
  key: 'search.json',
  format: 'json',
  currentVersion: 3,
  publishedContent: '{"a":1}',
});

describe('PublishConfigDialog', () => {
  afterEach(() => {
    calls.save.length = 0;
    calls.publish.length = 0;
    calls.conflict = false;
  });

  it('shows the diff, saves unsaved edits first and publishes with the expected version', async () => {
    const user = userEvent.setup();
    const onPublished = vi.fn();
    const onOpenChange = vi.fn();
    const state = {
      ...initialEditorState(item),
      values: { content: '{"a":2}', schema: '', description: '' },
    };
    render(
      <PublishConfigDialog
        open
        onOpenChange={onOpenChange}
        item={item}
        state={state}
        onPublished={onPublished}
      />,
    );

    expect(screen.getByTestId('diff-original')).toHaveTextContent('{"a":1}');
    expect(screen.getByTestId('diff-modified')).toHaveTextContent('{"a":2}');
    expect(screen.getByText('publish.savesDraftFirst')).toBeInTheDocument();

    await user.type(screen.getByRole('textbox'), 'raise limit');
    await user.click(screen.getByRole('button', { name: 'publish.confirm' }));

    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
    expect(calls.save).toEqual([expect.objectContaining({ id: 'cfg_1', content: '{"a":2}' })]);
    expect(calls.publish).toEqual([
      expect.objectContaining({ id: 'cfg_1', comment: 'raise limit', expectedVersion: 3 }),
    ]);
    expect(onPublished).toHaveBeenCalledWith(
      expect.objectContaining({ saved: expect.objectContaining({ sent: state.values }) }),
    );
  });

  it('prepares the diff only while open, not on every editor change or comment keystroke', async () => {
    prepared.count = 0;
    const user = userEvent.setup();
    const props = { onOpenChange: vi.fn(), item, onPublished: vi.fn() };
    const edited = (content: string) => ({
      ...initialEditorState(item),
      values: { content, schema: '', description: '' },
    });
    const { rerender } = render(<PublishConfigDialog {...props} open={false} state={edited('{"a":2}')} />);
    rerender(<PublishConfigDialog {...props} open={false} state={edited('{"a":22}')} />);
    expect(prepared.count).toBe(0);
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument();

    const state = edited('{"a":3}');
    rerender(<PublishConfigDialog {...props} open state={state} />);
    expect(screen.getByTestId('diff-modified')).toHaveTextContent('{"a":3}');
    const afterOpen = prepared.count;
    expect(afterOpen).toBeGreaterThan(0);

    await user.type(screen.getByRole('textbox'), 'note');
    expect(prepared.count).toBe(afterOpen);
  });

  it('disables publishing when the draft equals the published content', () => {
    render(
      <PublishConfigDialog
        open
        onOpenChange={vi.fn()}
        item={item}
        state={initialEditorState(item)}
        onPublished={vi.fn()}
      />,
    );
    expect(screen.getByRole('button', { name: 'publish.confirm' })).toBeDisabled();
    expect(screen.getByText('publish.noChanges')).toBeInTheDocument();
  });

  it('keeps the dialog open when publishing a stored draft fails', async () => {
    calls.conflict = true;
    const user = userEvent.setup();
    const onPublished = vi.fn();
    const onOpenChange = vi.fn();
    const stored = create(ConfigItemInfoSchema, { ...item, hasDraft: true, draftContent: '{"a":5}' });
    render(
      <PublishConfigDialog
        open
        onOpenChange={onOpenChange}
        item={stored}
        state={initialEditorState(stored)}
        onPublished={onPublished}
      />,
    );
    await user.click(screen.getByRole('button', { name: 'publish.confirm' }));
    await waitFor(() => expect(calls.publish).toHaveLength(1));
    expect(calls.save).toHaveLength(0);
    expect(onOpenChange).not.toHaveBeenCalledWith(false);
    expect(onPublished).not.toHaveBeenCalled();
  });
});
