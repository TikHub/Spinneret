import { type JsonObject } from '@bufbuild/protobuf';
import { EyeIcon, EyeOffIcon, FilePenLineIcon, LockIcon } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import { PERMISSIONS } from '@/app/auth/permissions';
import { PermissionButton } from '@/app/auth/PermissionGate';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { JsonView } from '@/components/JsonView';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { type Identity } from '@/gen/spinneret/v1/identity_admin_pb';
import { identityClient } from '@/lib/clients';

import { maskedFieldNames } from '../../payload';
import { REVEAL_DURATION_MS, type RevealedPayload } from '../../reveal';

export interface PayloadCardProps {
  identity: Identity;
  /** Payload as returned by GetIdentity (masked unless revealed). */
  maskedPayload: JsonObject | undefined;
  reveal: RevealedPayload;
  onEdit: () => void;
  /** Reports open dialogs so the page can pause auto refresh. */
  onDialogChange?: (open: boolean) => void;
}

/** Current payload with masked sensitive fields; "Reveal" shows clear text for 60 seconds after confirmation. */
export function PayloadCard({ identity, maskedPayload, reveal, onEdit, onDialogChange }: PayloadCardProps) {
  const { t } = useTranslation('identities');
  const [confirmOpen, setConfirmOpen] = useState(false);
  const revealed = reveal.payload !== undefined;
  const payload = reveal.payload ?? maskedPayload ?? {};
  const masked = maskedFieldNames(maskedPayload);

  const setConfirm = (open: boolean) => {
    setConfirmOpen(open);
    onDialogChange?.(open);
  };

  return (
    <Card>
      <CardHeader className="flex-row items-start gap-2">
        <div className="grid gap-1">
          <CardTitle className="flex items-center gap-2">
            {t('payload.title')}
            <Badge variant="outline" className="tabular font-normal">
              v{identity.payloadVersion}
            </Badge>
            {revealed ? (
              <Badge className="border-amber-500/30 bg-amber-500/10 font-normal text-amber-700 dark:text-amber-400">
                <EyeIcon />
                {t('payload.revealedFor', { seconds: reveal.secondsLeft })}
              </Badge>
            ) : (
              masked.length > 0 && (
                <Badge variant="muted" className="font-normal">
                  <LockIcon />
                  {t('payload.masked', { count: masked.length })}
                </Badge>
              )
            )}
          </CardTitle>
          <CardDescription>{revealed ? t('payload.revealedHint') : t('payload.maskedHint')}</CardDescription>
        </div>
        <CardAction>
          {revealed ? (
            <Button variant="outline" size="sm" onClick={reveal.hide}>
              <EyeOffIcon />
              {t('payload.hide')}
            </Button>
          ) : (
            <PermissionButton
              permission={PERMISSIONS.identityReveal}
              site={identity.site}
              variant="outline"
              size="sm"
              onClick={() => setConfirm(true)}
            >
              <EyeIcon />
              {t('payload.reveal')}
            </PermissionButton>
          )}
          <PermissionButton
            permission={PERMISSIONS.identityWrite}
            site={identity.site}
            variant="outline"
            size="sm"
            onClick={onEdit}
          >
            <FilePenLineIcon />
            {t('payload.edit')}
          </PermissionButton>
        </CardAction>
      </CardHeader>
      <CardContent>
        {Object.keys(payload).length === 0 ? (
          <p className="text-sm text-muted-foreground">{t('payload.empty')}</p>
        ) : (
          <JsonView
            value={payload}
            collapseDepth={3}
            copyable={revealed || masked.length === 0}
            maxHeight="24rem"
          />
        )}
      </CardContent>
      <ConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirm}
        title={t('payload.revealTitle')}
        description={t('payload.revealDescription', { seconds: REVEAL_DURATION_MS / 1000 })}
        confirmText="reveal"
        confirmLabel={t('payload.reveal')}
        onConfirm={async () => {
          const res = await identityClient.getIdentity({ id: identity.id, reveal: true });
          // The server silently falls back to the masked payload when it cannot reveal
          // (no identity:reveal on the site, or the payload cannot be decrypted).
          if (!res.revealed) {
            toast.error(t('payload.revealRefused'));
            return;
          }
          reveal.reveal(res.payload ?? {});
        }}
      />
    </Card>
  );
}
