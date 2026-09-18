import { type JsonObject } from '@bufbuild/protobuf';
import { TriangleAlertIcon } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import { ConfirmDialog } from '@/components/ConfirmDialog';
import { CodeEditor } from '@/components/editor/CodeEditor';
import { Button } from '@/components/ui/button';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { type Identity } from '@/gen/spinneret/v1/identity_admin_pb';
import { identityClient } from '@/lib/clients';

import { useInvalidateIdentityData } from '../../notify';
import { containsMaskedValues, parsePayloadJson, stringifyPayload } from '../../payload';

export interface PayloadEditDialogProps {
  identity: Identity;
  /** Starting payload: the revealed payload when available, otherwise the masked one. */
  initialPayload: JsonObject | undefined;
  /** Whether initialPayload is clear text. */
  revealed: boolean;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Called after the new payload version was stored. */
  onSaved: () => void;
}

/** JSON editor for a new payload version (UpdateIdentityPayload), confirming the state reset. */
export function PayloadEditDialog({
  identity,
  initialPayload,
  revealed,
  open,
  onOpenChange,
  onSaved,
}: PayloadEditDialogProps) {
  const { t } = useTranslation('identities');
  const invalidate = useInvalidateIdentityData();
  const [text, setText] = useState(() => stringifyPayload(initialPayload));
  // The card re-masks after the reveal window; the editor keeps the text it was opened with.
  const [openedRevealed] = useState(revealed);
  const [confirmOpen, setConfirmOpen] = useState(false);
  const parsed = parsePayloadJson(text);
  const hasMasked = parsed.ok && containsMaskedValues(parsed.value);
  const parseError = parsed.ok
    ? undefined
    : t(`payload.errors.${parsed.error}`, { detail: 'detail' in parsed ? (parsed.detail ?? '') : '' });

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>{t('payload.editTitle')}</DialogTitle>
          <DialogDescription>
            {t('payload.editDescription', { version: identity.payloadVersion + 1 })}
          </DialogDescription>
        </DialogHeader>
        {!openedRevealed && (
          <p className="flex items-start gap-2 rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-sm text-amber-700 dark:text-amber-400">
            <TriangleAlertIcon className="mt-0.5 size-4 shrink-0" aria-hidden />
            {t('payload.editMaskedWarning')}
          </p>
        )}
        <CodeEditor
          value={text}
          onChange={setText}
          language="json"
          height={380}
          path={`identity-payload-${identity.id}.json`}
          aria-label={t('payload.editorLabel')}
        />
        {parseError && (
          <p role="alert" className="text-xs text-destructive">
            {parseError}
          </p>
        )}
        {hasMasked && (
          <p role="alert" className="text-xs text-destructive">
            {t('payload.errors.masked')}
          </p>
        )}
        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>
            {t('common:actions.cancel')}
          </Button>
          <Button disabled={!parsed.ok || hasMasked} onClick={() => setConfirmOpen(true)}>
            {t('payload.saveVersion')}
          </Button>
        </DialogFooter>
        <ConfirmDialog
          open={confirmOpen}
          onOpenChange={setConfirmOpen}
          title={t('payload.confirmTitle')}
          description={t('payload.confirmDescription')}
          confirmLabel={t('payload.saveVersion')}
          onConfirm={async () => {
            if (!parsed.ok) return;
            await identityClient.updateIdentityPayload({ id: identity.id, payload: parsed.value });
            toast.success(t('payload.saved'));
            invalidate();
            onSaved();
            onOpenChange(false);
          }}
        />
      </DialogContent>
    </Dialog>
  );
}
