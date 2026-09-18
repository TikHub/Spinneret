/** Languages supported by the console editors. */
export type EditorLanguage = 'yaml' | 'json' | 'plaintext';

/** A validation marker (1-based lines and columns, as reported by the API). */
export interface EditorMarker {
  line: number;
  column?: number;
  endLine?: number;
  endColumn?: number;
  message: string;
  severity?: 'error' | 'warning' | 'info';
}

export interface CodeEditorProps {
  value: string;
  onChange?: (value: string) => void;
  language?: EditorLanguage;
  readOnly?: boolean;
  /** CSS height; defaults to 360px. */
  height?: number | string;
  /** Validation markers shown as squiggles and in the overview ruler. */
  markers?: readonly EditorMarker[];
  /** Model URI path (e.g. "policy.yaml"); lets JSON schemas match by fileMatch. */
  path?: string;
  className?: string;
  /** Extra Monaco editor options. */
  options?: Record<string, unknown>;
  'aria-label'?: string;
}

export interface DiffViewProps {
  original: string;
  modified: string;
  language?: EditorLanguage;
  height?: number | string;
  /** Side by side (default) or inline diff. */
  sideBySide?: boolean;
  className?: string;
  /** Allow editing the modified side. */
  editable?: boolean;
  onModifiedChange?: (value: string) => void;
}
