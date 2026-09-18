import { FileUpIcon, FlaskConicalIcon, LoaderCircleIcon, UploadIcon } from 'lucide-react';
import { useMemo, useState, type ChangeEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import { PERMISSIONS } from '@/app/auth/permissions';
import { PermissionButton } from '@/app/auth/PermissionGate';
import { Button } from '@/components/ui/button';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { FormField } from '@/components/ui/form';
import { Input } from '@/components/ui/input';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs';
import { Textarea } from '@/components/ui/textarea';
import { type ImportProxiesResponse } from '@/gen/spinneret/v1/proxy_admin_pb';
import { describeError, errorMessage } from '@/lib/errors';
import { formatBytes, formatNumber } from '@/lib/format';

import {
  buildImportDefaults,
  countDataLines,
  EMPTY_IMPORT_DEFAULTS,
  formatFromFileName,
  IMPORT_FORMATS,
  importFingerprint,
  MAX_IMPORT_BYTES,
  summarizeImportData,
  validateImport,
  type ImportDefaultsForm,
  type ImportFormat,
} from '../importProxies';
import { useImportProxies } from '../useProxies';
import { ImportDefaultsFields } from './ImportDefaultsFields';
import { ImportResultView } from './ImportResultView';

type Source = 'paste' | 'file';

interface LoadedFile {
  name: string;
  size: number;
  data: string;
}

interface RunResult {
  response: ImportProxiesResponse;
  dryRun: boolean;
  fingerprint: string;
}

export interface ImportProxiesDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

/** Import proxies from URL lines, JSON Lines or CSV: defaults, dry run, then import. */
export function ImportProxiesDialog({ open, onOpenChange }: ImportProxiesDialogProps) {
  const { t, i18n } = useTranslation('proxies');
  const [format, setFormat] = useState<ImportFormat>('lines');
  const [source, setSource] = useState<Source>('paste');
  const [pasted, setPasted] = useState('');
  const [file, setFile] = useState<LoadedFile>();
  const [defaults, setDefaults] = useState<ImportDefaultsForm>(EMPTY_IMPORT_DEFAULTS);
  const [submitted, setSubmitted] = useState(false);
  const [dryRun, setDryRun] = useState<RunResult>();
  const [imported, setImported] = useState<RunResult>();
  const mutation = useImportProxies();

  const data = source === 'paste' ? pasted : (file?.data ?? '');
  const summary = useMemo(() => summarizeImportData(data), [data]);
  const lineCount = useMemo(() => countDataLines(data, format), [data, format]);
  const issues = validateImport(summary, defaults);
  const fingerprint = importFingerprint(format, summary, defaults);
  const dryRunCurrent = dryRun?.fingerprint === fingerprint;
  const alreadyImported = imported?.fingerprint === fingerprint;
  const lastResult = [dryRun, imported]
    .filter((r): r is RunResult => r !== undefined && r.fingerprint === fingerprint)
    .at(-1);
  const busy = mutation.isPending;

  const onFile = async (event: ChangeEvent<HTMLInputElement>) => {
    const selected = event.target.files?.[0];
    event.target.value = '';
    if (!selected) return;
    if (selected.size > MAX_IMPORT_BYTES) {
      toast.error(t('import.errors.data_too_large'));
      return;
    }
    try {
      const text = await selected.text();
      setFile({ name: selected.name, size: selected.size, data: text });
      const detected = formatFromFileName(selected.name);
      if (detected) setFormat(detected);
    } catch {
      toast.error(t('import.fileReadFailed'));
    }
  };

  const run = (asDryRun: boolean) => {
    setSubmitted(true);
    if (issues.length > 0) return;
    mutation.mutate(
      { format, data, defaults: buildImportDefaults(defaults), dryRun: asDryRun },
      {
        onSuccess: (response) => {
          const result = { response, dryRun: asDryRun, fingerprint };
          const counts = {
            created: response.created,
            updated: response.updated,
            unchanged: response.unchanged,
            failed: response.failed.length,
          };
          if (asDryRun) {
            setDryRun(result);
            toast.info(t('import.dryRunDone', counts));
          } else {
            setImported(result);
            if (counts.failed > 0) toast.warning(t('import.done', counts));
            else toast.success(t('import.done', counts));
          }
        },
        onError: (err) => toast.error(errorMessage(err, t)),
      },
    );
  };

  const dataError = submitted
    ? issues.includes('data_required')
      ? t('import.errors.data_required')
      : issues.includes('data_too_large')
        ? t('import.errors.data_too_large')
        : undefined
    : undefined;
  const apiError = mutation.error ? describeError(mutation.error, t) : undefined;

  return (
    <Dialog open={open} onOpenChange={(next) => !busy && onOpenChange(next)}>
      <DialogContent className="sm:max-w-4xl">
        <DialogHeader>
          <DialogTitle>{t('import.title')}</DialogTitle>
          <DialogDescription>{t('import.description')}</DialogDescription>
        </DialogHeader>

        <div className="grid gap-5 lg:grid-cols-2">
          <div className="grid content-start gap-3">
            <FormField label={t('import.format')} description={t(`import.formatHelp.${format}`)}>
              <Select value={format} onValueChange={(v) => setFormat(v as ImportFormat)} disabled={busy}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {IMPORT_FORMATS.map((f) => (
                    <SelectItem key={f} value={f}>
                      {t(`import.formats.${f}`)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </FormField>
            <Tabs value={source} onValueChange={(v) => setSource(v as Source)}>
              <TabsList>
                <TabsTrigger value="paste">{t('import.paste')}</TabsTrigger>
                <TabsTrigger value="file">{t('import.file')}</TabsTrigger>
              </TabsList>
              <TabsContent value="paste">
                <FormField label={t('import.data')} error={dataError}>
                  <Textarea
                    value={pasted}
                    onChange={(e) => setPasted(e.target.value)}
                    placeholder={t(`import.placeholders.${format}`)}
                    className="min-h-56 font-mono text-xs"
                    spellCheck={false}
                    autoComplete="off"
                    disabled={busy}
                  />
                </FormField>
              </TabsContent>
              <TabsContent value="file" className="grid gap-2">
                <FormField label={t('import.chooseFile')} error={dataError}>
                  <Input
                    type="file"
                    accept=".txt,.lst,.list,.csv,.jsonl,.ndjson,.json,text/plain,text/csv,application/json"
                    onChange={(e) => void onFile(e)}
                    disabled={busy}
                  />
                </FormField>
                {file && (
                  <p className="flex items-center gap-2 text-sm">
                    <FileUpIcon className="size-4 text-muted-foreground" aria-hidden />
                    <span className="truncate font-medium">{file.name}</span>
                    <span className="text-muted-foreground">{formatBytes(file.size)}</span>
                  </p>
                )}
              </TabsContent>
            </Tabs>
            {data && (
              <p className="text-xs text-muted-foreground">
                {t('import.lines', {
                  count: lineCount,
                  formatted: formatNumber(lineCount, undefined, i18n.language),
                })}
              </p>
            )}
            <p className="text-xs text-muted-foreground">{t('import.credentialsNote')}</p>
          </div>
          <ImportDefaultsFields
            value={defaults}
            onChange={setDefaults}
            issues={submitted ? issues : []}
            disabled={busy}
          />
        </div>

        {lastResult && <ImportResultView result={lastResult.response} dryRun={lastResult.dryRun} />}
        {apiError && (
          <p role="alert" className="text-sm text-destructive">
            {apiError.title}
            {apiError.detail && apiError.detail !== apiError.title && `: ${apiError.detail}`}
          </p>
        )}

        <DialogFooter className="items-center">
          {!dryRunCurrent && data !== '' && (
            <span className="mr-auto text-xs text-muted-foreground">{t('import.dryRunFirst')}</span>
          )}
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={busy}>
            {t('common:actions.close')}
          </Button>
          <Button variant="secondary" onClick={() => run(true)} disabled={busy}>
            {busy && mutation.variables?.dryRun ? (
              <LoaderCircleIcon className="animate-spin" />
            ) : (
              <FlaskConicalIcon />
            )}
            {t('import.dryRun')}
          </Button>
          <PermissionButton
            permission={PERMISSIONS.proxyWrite}
            onClick={() => run(false)}
            disabled={busy || !dryRunCurrent || alreadyImported}
          >
            {busy && mutation.variables?.dryRun === false ? (
              <LoaderCircleIcon className="animate-spin" />
            ) : (
              <UploadIcon />
            )}
            {t('import.import')}
          </PermissionButton>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
