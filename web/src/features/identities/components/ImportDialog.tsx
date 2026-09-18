import { useMutation } from '@tanstack/react-query';
import { FlaskConicalIcon, LoaderCircleIcon, UploadIcon } from 'lucide-react';
import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import { useAuth } from '@/app/auth/AuthContext';
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
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { type ImportIdentitiesResponse } from '@/gen/spinneret/v1/identity_admin_pb';
import { identityClient } from '@/lib/clients';
import { errorMessage } from '@/lib/errors';
import { formatBytes, formatNumber } from '@/lib/format';
import { sensitiveMutation } from '@/lib/sensitiveMutation';

import {
  countImportRows,
  IMPORT_FORMATS,
  IMPORT_MAX_BYTES,
  IMPORT_MAX_ROWS,
  IMPORT_MODES,
  sameImportInput,
  utf8ByteLength,
  validateImportData,
  type ImportFormat,
  type ImportInputKey,
  type ImportMode,
} from '../importData';
import { useInvalidateIdentityData } from '../notify';
import { IdentityTypeSelect } from './IdentityTypeSelect';
import { ImportResultView } from './ImportResultView';
import { ImportSourceField, type ImportSourceValue } from './ImportSourceField';
import { SiteSelect } from './SiteSelect';

export interface ImportDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  defaultSite?: string;
  defaultType?: string;
}

interface RunVariables {
  dryRun: boolean;
  key: ImportInputKey;
  data: string;
}

const EMPTY_SOURCE: ImportSourceValue = {
  source: 'file',
  fileName: '',
  fileSize: 0,
  fileData: '',
  pasteData: '',
};

/** Import identities from JSON Lines or CSV: validate with a dry run, then import (ImportIdentities). */
export function ImportDialog({ open, onOpenChange, defaultSite = '', defaultType = '' }: ImportDialogProps) {
  const { t, i18n } = useTranslation('identities');
  const { namespaceName, can } = useAuth();
  const invalidate = useInvalidateIdentityData();
  const [site, setSite] = useState(defaultSite);
  const [type, setType] = useState(defaultType);
  const [format, setFormat] = useState<ImportFormat>('jsonl');
  const [mode, setMode] = useState<ImportMode>('upsert');
  const [source, setSource] = useState<ImportSourceValue>(EMPTY_SOURCE);
  const [dataVersion, setDataVersion] = useState(0);
  const [validated, setValidated] = useState<ImportInputKey>();
  const [result, setResult] = useState<{ response: ImportIdentitiesResponse; dryRun: boolean }>();

  const data = source.source === 'file' ? source.fileData : source.pasteData;
  const bytes = useMemo(
    () => (source.source === 'file' ? source.fileSize : utf8ByteLength(source.pasteData)),
    [source.source, source.fileSize, source.pasteData],
  );
  const rows = useMemo(() => countImportRows(data, format), [data, format]);
  const dataError = useMemo(() => validateImportData(data, format), [data, format]);
  const key: ImportInputKey = { site, type, format, mode, dataVersion };
  const canWrite = can(PERMISSIONS.identityWrite, site || undefined);
  const ready = Boolean(namespaceName) && site !== '' && type !== '' && dataError === undefined;

  // The variables hold identity payloads (credentials).
  const run = useMutation(
    sensitiveMutation({
      mutationFn: (vars: RunVariables) =>
        identityClient.importIdentities({
          namespace: namespaceName ?? '',
          site: vars.key.site,
          type: vars.key.type,
          format: vars.key.format,
          mode: vars.key.mode,
          data: vars.data,
          dryRun: vars.dryRun,
        }),
      onSuccess: (response, vars) => {
        setResult({ response, dryRun: vars.dryRun });
        const counts = {
          created: response.created,
          updated: response.updated,
          unchanged: response.unchanged,
          failed: response.failed.length,
        };
        if (vars.dryRun) {
          setValidated(vars.key);
          if (counts.failed > 0) toast.warning(t('import.toastValidatedWithFailures', counts));
          else toast.success(t('import.toastValidated', counts));
        } else {
          setValidated(undefined);
          invalidate();
          toast.success(t('import.toastImported', counts));
        }
      },
      onError: (err) => toast.error(errorMessage(err, t)),
    }),
  );

  const changeSource = (next: ImportSourceValue) => {
    const nextData = next.source === 'file' ? next.fileData : next.pasteData;
    if (nextData !== data) setDataVersion((v) => v + 1);
    setSource(next);
  };

  const importable =
    sameImportInput(validated, key) &&
    result?.dryRun === true &&
    result.response.created + result.response.updated + result.response.unchanged > 0;

  const errorText =
    dataError === 'tooLarge'
      ? t('import.dataTooLarge', { max: formatBytes(IMPORT_MAX_BYTES) })
      : dataError === 'tooManyRows'
        ? t('import.tooManyRows', { max: formatNumber(IMPORT_MAX_ROWS, undefined, i18n.language) })
        : undefined;

  return (
    <Dialog open={open} onOpenChange={(next) => !run.isPending && onOpenChange(next)}>
      <DialogContent className="sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>{t('import.title')}</DialogTitle>
          <DialogDescription>{t('import.description')}</DialogDescription>
        </DialogHeader>
        <div className="grid gap-3 sm:grid-cols-2">
          <FormField label={t('fields.site')} required>
            <SiteSelect
              value={site}
              onChange={(next) => {
                if (next !== site) setType('');
                setSite(next);
              }}
              disabled={run.isPending}
            />
          </FormField>
          <FormField label={t('fields.type')} required>
            <IdentityTypeSelect
              site={site}
              value={type}
              onChange={(name) => setType(name)}
              disabled={run.isPending || site === ''}
            />
          </FormField>
          <FormField label={t('import.format')} description={t(`import.formatHints.${format}`)}>
            <Select
              value={format}
              onValueChange={(v) => setFormat(v as ImportFormat)}
              disabled={run.isPending}
            >
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
          <FormField label={t('import.mode')} description={t(`import.modeHints.${mode}`)}>
            <Select value={mode} onValueChange={(v) => setMode(v as ImportMode)} disabled={run.isPending}>
              <SelectTrigger className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {IMPORT_MODES.map((m) => (
                  <SelectItem key={m} value={m}>
                    {t(`import.modes.${m}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </FormField>
        </div>
        <ImportSourceField
          value={source}
          onChange={changeSource}
          onDetectFormat={setFormat}
          disabled={run.isPending}
        />
        <p className="text-xs text-muted-foreground">
          {t('import.dataStats', {
            rows: formatNumber(rows, undefined, i18n.language),
            size: formatBytes(bytes),
            max: formatBytes(IMPORT_MAX_BYTES),
          })}
          {errorText && <span className="ml-2 text-destructive">{errorText}</span>}
        </p>
        {result && <ImportResultView result={result.response} dryRun={result.dryRun} />}
        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)} disabled={run.isPending}>
            {t('common:actions.close')}
          </Button>
          <PermissionButton
            permission={PERMISSIONS.identityWrite}
            site={site || undefined}
            variant="outline"
            disabled={!ready || run.isPending}
            onClick={() => run.mutate({ dryRun: true, key, data })}
          >
            {run.isPending && run.variables.dryRun ? (
              <LoaderCircleIcon className="animate-spin" />
            ) : (
              <FlaskConicalIcon />
            )}
            {t('import.dryRun')}
          </PermissionButton>
          <PermissionButton
            permission={PERMISSIONS.identityWrite}
            site={site || undefined}
            disabled={!ready || !importable || !canWrite || run.isPending}
            onClick={() => run.mutate({ dryRun: false, key, data })}
            title={importable ? undefined : t('import.dryRunFirst')}
          >
            {run.isPending && !run.variables.dryRun ? (
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
