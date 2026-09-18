import { FileUpIcon, XIcon } from 'lucide-react';
import { useId, useRef, useState, type ChangeEvent } from 'react';
import { useTranslation } from 'react-i18next';

import { Button } from '@/components/ui/button';
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs';
import { Textarea } from '@/components/ui/textarea';
import { formatBytes } from '@/lib/format';

import { detectImportFormat, IMPORT_MAX_BYTES, type ImportFormat } from '../importData';

export type ImportSource = 'file' | 'paste';

export interface ImportSourceValue {
  source: ImportSource;
  fileName: string;
  /** File size in bytes. */
  fileSize: number;
  fileData: string;
  pasteData: string;
}

export interface ImportSourceFieldProps {
  value: ImportSourceValue;
  onChange: (value: ImportSourceValue) => void;
  /** Called when a file name reveals the format. */
  onDetectFormat: (format: ImportFormat) => void;
  disabled?: boolean;
}

/** Import data from a file upload or pasted text (JSON Lines or CSV, at most 32 MiB). */
export function ImportSourceField({ value, onChange, onDetectFormat, disabled }: ImportSourceFieldProps) {
  const { t } = useTranslation('identities');
  const inputId = useId();
  const fileInput = useRef<HTMLInputElement>(null);
  const [fileError, setFileError] = useState<string>();
  const [reading, setReading] = useState(false);

  const onFile = async (event: ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0];
    event.target.value = '';
    if (!file) return;
    setFileError(undefined);
    if (file.size > IMPORT_MAX_BYTES) {
      setFileError(
        t('import.fileTooLarge', { size: formatBytes(file.size), max: formatBytes(IMPORT_MAX_BYTES) }),
      );
      return;
    }
    setReading(true);
    try {
      const text = await file.text();
      const format = detectImportFormat(file.name);
      if (format) onDetectFormat(format);
      onChange({ ...value, source: 'file', fileName: file.name, fileSize: file.size, fileData: text });
    } catch {
      setFileError(t('import.fileReadFailed'));
    } finally {
      setReading(false);
    }
  };

  return (
    <Tabs
      value={value.source}
      onValueChange={(source) => onChange({ ...value, source: source as ImportSource })}
      className="gap-2"
    >
      <TabsList>
        <TabsTrigger value="file" disabled={disabled}>
          {t('import.sourceFile')}
        </TabsTrigger>
        <TabsTrigger value="paste" disabled={disabled}>
          {t('import.sourcePaste')}
        </TabsTrigger>
      </TabsList>
      <TabsContent value="file">
        <div className="flex flex-wrap items-center gap-3 rounded-md border border-dashed p-4">
          <input
            ref={fileInput}
            id={inputId}
            type="file"
            accept=".jsonl,.ndjson,.json,.csv,.txt,text/csv,application/json"
            className="sr-only"
            onChange={(e) => void onFile(e)}
            disabled={disabled || reading}
          />
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={disabled || reading}
            onClick={() => fileInput.current?.click()}
          >
            <FileUpIcon />
            {reading ? t('import.reading') : t('import.chooseFile')}
          </Button>
          {value.fileName ? (
            <span className="flex min-w-0 items-center gap-1 text-sm">
              <span className="truncate font-mono">{value.fileName}</span>
              <span className="text-muted-foreground">({formatBytes(value.fileSize)})</span>
              <Button
                type="button"
                variant="ghost"
                size="icon-sm"
                className="size-6"
                aria-label={t('import.clearFile')}
                disabled={disabled}
                onClick={() => onChange({ ...value, fileName: '', fileSize: 0, fileData: '' })}
              >
                <XIcon />
              </Button>
            </span>
          ) : (
            <label htmlFor={inputId} className="text-sm text-muted-foreground">
              {t('import.fileHint')}
            </label>
          )}
        </div>
        {fileError && (
          <p role="alert" className="mt-1 text-xs text-destructive">
            {fileError}
          </p>
        )}
      </TabsContent>
      <TabsContent value="paste">
        <Textarea
          value={value.pasteData}
          onChange={(e) => onChange({ ...value, pasteData: e.target.value })}
          rows={8}
          spellCheck={false}
          disabled={disabled}
          className="max-h-72 font-mono text-xs"
          placeholder={t('import.pastePlaceholder')}
          aria-label={t('import.sourcePaste')}
        />
      </TabsContent>
    </Tabs>
  );
}
