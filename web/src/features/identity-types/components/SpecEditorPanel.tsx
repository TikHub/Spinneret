import { CircleAlertIcon, FileCode2Icon } from 'lucide-react';
import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';

import { ConfirmDialog } from '@/components/ConfirmDialog';
import { CodeEditor } from '@/components/editor/CodeEditor';
import { Button } from '@/components/ui/button';
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';

import { IDENTITY_TYPE_EXAMPLES, type IdentityTypeExample } from '../examples';
import { specErrorMarkers, splitSpecErrors } from '../specErrors';

export interface SpecEditorPanelProps {
  value: string;
  onChange: (value: string) => void;
  /** Validation problems reported by the server for the last save. */
  serverErrors: readonly string[];
  readOnly?: boolean;
  editorPath: string;
}

/** YAML spec editor with example templates and server validation errors inline. */
export function SpecEditorPanel({
  value,
  onChange,
  serverErrors,
  readOnly,
  editorPath,
}: SpecEditorPanelProps) {
  const { t } = useTranslation('identity-types');
  const [pendingExample, setPendingExample] = useState<IdentityTypeExample>();
  const problems = useMemo(() => splitSpecErrors(serverErrors), [serverErrors]);
  const lineCount = useMemo(() => value.split('\n').length, [value]);
  // Stable markers: the editor re-applies them whenever the array identity changes.
  const markers = useMemo(() => specErrorMarkers(serverErrors, lineCount), [serverErrors, lineCount]);

  const insert = (example: IdentityTypeExample) => {
    const text = IDENTITY_TYPE_EXAMPLES[example];
    if (value.trim() === '' || value === text) onChange(text);
    else setPendingExample(example);
  };

  return (
    <div className="grid gap-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-xs text-muted-foreground">
          {t('editor.specHint', { placeholder: '{{ field }}' })}
        </p>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="outline" size="sm" disabled={readOnly}>
              <FileCode2Icon />
              {t('editor.insertExample')}
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem onSelect={() => insert('webCookie')}>
              {t('editor.examples.webCookie')}
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={() => insert('appDevice')}>
              {t('editor.examples.appDevice')}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
      <CodeEditor
        value={value}
        onChange={onChange}
        language="yaml"
        height={420}
        readOnly={readOnly}
        markers={markers}
        path={editorPath}
        aria-label={t('editor.specLabel')}
      />
      {problems.length > 0 && (
        <ul
          role="alert"
          className="grid max-h-40 gap-1 overflow-auto rounded-md border border-destructive/30 bg-destructive/5 p-3 text-sm text-destructive"
        >
          {problems.map((problem) => (
            <li key={problem} className="flex items-start gap-2 break-words">
              <CircleAlertIcon className="mt-0.5 size-4 shrink-0" aria-hidden />
              {problem}
            </li>
          ))}
        </ul>
      )}
      <ConfirmDialog
        open={pendingExample !== undefined}
        onOpenChange={(open) => !open && setPendingExample(undefined)}
        title={t('editor.replaceTitle')}
        description={t('editor.replaceDescription')}
        confirmLabel={t('editor.replace')}
        destructive
        onConfirm={() => {
          if (pendingExample) onChange(IDENTITY_TYPE_EXAMPLES[pendingExample]);
          setPendingExample(undefined);
        }}
      />
    </div>
  );
}
