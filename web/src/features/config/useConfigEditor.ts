import { useCallback, useMemo, useState } from 'react';

import { type ConfigItemInfo } from '@/gen/spinneret/v1/config_admin_pb';

import {
  checkJsonSyntax,
  formatSupportsSchema,
  MAX_CONFIG_CONTENT_BYTES,
  utf8ByteLength,
  validateSchemaDocument,
  type JsonCheck,
  type SchemaError,
} from './configModel';
import {
  applySaved,
  hasRemoteChanges,
  initialEditorState,
  isDirty,
  reconcileEditorState,
  type ConfigDraftValues,
  type ConfigEditorState,
} from './draftState';
import { scanSecretRefs, type SecretRefScan } from './secretRefs';

/** Client-side checks of the edited values. */
export interface ConfigValidation {
  json: JsonCheck | undefined;
  refs: SecretRefScan;
  schemaError: SchemaError | undefined;
  contentBytes: number;
  tooLarge: boolean;
  /** True when a check blocks saving or publishing. */
  blocking: boolean;
}

/** Checks the schema document of an item (undefined for formats without schemas). */
export function validateSchema(format: string, schema: string): SchemaError | undefined {
  return formatSupportsSchema(format) ? validateSchemaDocument(schema) : undefined;
}

/** Checks edited content; `schemaError` comes from validateSchema (memoized separately). */
export function validateValues(
  format: string,
  content: string,
  schemaError: SchemaError | undefined,
): ConfigValidation {
  const json = format === 'json' ? checkJsonSyntax(content) : undefined;
  const refs = scanSecretRefs(content);
  const contentBytes = utf8ByteLength(content);
  const tooLarge = contentBytes > MAX_CONFIG_CONTENT_BYTES;
  const blocking =
    (json !== undefined && !json.ok) || refs.problems.length > 0 || schemaError !== undefined || tooLarge;
  return { json, refs, schemaError, contentBytes, tooLarge, blocking };
}

export interface ConfigEditor {
  state: ConfigEditorState;
  dirty: boolean;
  /** The server item changed while local edits are pending. */
  remoteChanged: boolean;
  validation: ConfigValidation;
  setField: (field: keyof ConfigDraftValues, value: string) => void;
  discard: () => void;
  markSaved: (sent: ConfigDraftValues, saved: ConfigItemInfo) => void;
}

/** Local draft editing state of a config item, reconciled with server updates. */
export function useConfigEditor(item: ConfigItemInfo | undefined): ConfigEditor {
  const [stored, setStored] = useState<ConfigEditorState>(() => initialEditorState(item));
  const state = reconcileEditorState(stored, item);
  if (state !== stored) setStored(state);

  const setField = useCallback((field: keyof ConfigDraftValues, value: string) => {
    setStored((prev) => ({ ...prev, values: { ...prev.values, [field]: value } }));
  }, []);
  const discard = useCallback(() => setStored(initialEditorState(item)), [item]);
  const markSaved = useCallback((sent: ConfigDraftValues, saved: ConfigItemInfo) => {
    setStored((prev) => applySaved(prev, sent, saved));
  }, []);

  const format = item?.format ?? 'text';
  const { content, schema } = state.values;
  // Content changes on every keystroke; the schema (up to 1 MiB) is parsed only when it changes.
  const schemaError = useMemo(() => validateSchema(format, schema), [format, schema]);
  const validation = useMemo(
    () => validateValues(format, content, schemaError),
    [format, content, schemaError],
  );

  return {
    state,
    dirty: isDirty(state),
    remoteChanged: hasRemoteChanges(state, item),
    validation,
    setField,
    discard,
    markSaved,
  };
}
