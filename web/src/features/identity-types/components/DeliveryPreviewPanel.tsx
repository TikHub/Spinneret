import { useMutation } from '@tanstack/react-query';
import { CircleAlertIcon, LoaderCircleIcon, PlayIcon } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import { useAuth } from '@/app/auth/AuthContext';
import { CodeEditor } from '@/components/editor/CodeEditor';
import { JsonView } from '@/components/JsonView';
import { Button } from '@/components/ui/button';
import { parsePayloadJson } from '@/features/identities/payload';
import { type PreviewDeliveryResponse } from '@/gen/spinneret/v1/identity_admin_pb';
import { identityClient } from '@/lib/clients';
import { errorMessage } from '@/lib/errors';
import { sensitiveMutation } from '@/lib/sensitiveMutation';

import { CredentialPanel } from './CredentialPanel';

export type PreviewSource = { case: 'typeId'; value: string } | { case: 'specYaml'; value: string };

export interface DeliveryPreviewPanelProps {
  /** Site the draft spec is rendered for. */
  site: string;
  source: PreviewSource;
  /** Sample payload JSON (controlled so it survives tab switches). */
  payload: string;
  onPayloadChange: (payload: string) => void;
  result: PreviewDeliveryResponse | undefined;
  onResult: (result: PreviewDeliveryResponse) => void;
  /** Unique editor model path. */
  editorPath: string;
}

/** Sample payload → PreviewDelivery → rendered credential, normalized payload and errors. */
export function DeliveryPreviewPanel({
  site,
  source,
  payload,
  onPayloadChange,
  result,
  onResult,
  editorPath,
}: DeliveryPreviewPanelProps) {
  const { t } = useTranslation('identity-types');
  const { namespaceName } = useAuth();
  const parsed = parsePayloadJson(payload);

  // The sample payload and the rendered delivery are credentials.
  const preview = useMutation(
    sensitiveMutation({
      mutationFn: () =>
        identityClient.previewDelivery({
          namespace: namespaceName ?? '',
          site: source.case === 'specYaml' ? site : '',
          source,
          payloadJson: payload,
        }),
      onSuccess: onResult,
      onError: (err) => toast.error(errorMessage(err, t)),
    }),
  );

  const parseError = parsed.ok ? undefined : t(`preview.payloadErrors.${parsed.error}`);

  return (
    <div className="grid gap-4 lg:grid-cols-2">
      <div className="grid content-start gap-2">
        <div className="flex items-center justify-between gap-2">
          <h3 className="text-sm font-medium">{t('preview.payload')}</h3>
          <Button
            size="sm"
            onClick={() => preview.mutate()}
            disabled={!parsed.ok || !namespaceName || source.value.trim() === '' || preview.isPending}
          >
            {preview.isPending ? <LoaderCircleIcon className="animate-spin" /> : <PlayIcon />}
            {t('preview.render')}
          </Button>
        </div>
        <CodeEditor
          value={payload}
          onChange={onPayloadChange}
          language="json"
          height={340}
          path={editorPath}
          aria-label={t('preview.payload')}
        />
        {parseError && payload.trim() !== '' ? (
          <p role="alert" className="text-xs text-destructive">
            {parseError}
          </p>
        ) : (
          <p className="text-xs text-muted-foreground">{t('preview.payloadHint')}</p>
        )}
      </div>
      <div className="grid content-start gap-3" aria-live="polite">
        <h3 className="text-sm font-medium">{t('preview.credential')}</h3>
        {!result ? (
          <p className="rounded-md border border-dashed px-3 py-8 text-center text-sm text-muted-foreground">
            {t('preview.notRendered')}
          </p>
        ) : result.errors.length > 0 ? (
          <ul className="grid gap-1 rounded-md border border-destructive/30 bg-destructive/5 p-3 text-sm text-destructive">
            {result.errors.map((error, index) => (
              <li key={`${index}-${error}`} className="flex items-start gap-2 break-words">
                <CircleAlertIcon className="mt-0.5 size-4 shrink-0" aria-hidden />
                {error}
              </li>
            ))}
          </ul>
        ) : (
          <>
            <CredentialPanel credential={result.credential} />
            <section className="grid gap-1.5">
              <h4 className="text-sm font-medium">{t('preview.normalized')}</h4>
              <JsonView value={result.normalizedPayload ?? {}} collapseDepth={3} />
            </section>
          </>
        )}
      </div>
    </div>
  );
}
