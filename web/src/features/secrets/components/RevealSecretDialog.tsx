import { ShieldAlertIcon } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';

import { ConfirmDialog } from '@/components/ConfirmDialog';
import { FormField } from '@/components/ui/form';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { type SecretInfo } from '@/gen/spinneret/v1/secret_admin_pb';
import { secretAdminClient } from '@/lib/clients';
import { formatDateTime } from '@/lib/time';

import { useRecentSecretVersions } from '../useSecretsApi';
import { type RevealedSecret } from '../useRevealTimer';

/** Text typed to confirm a reveal. */
export const REVEAL_CONFIRM_TEXT = 'reveal';
const CURRENT = 'current';

export interface RevealSecretDialogProps {
  secret: SecretInfo | undefined;
  onOpenChange: (open: boolean) => void;
  onRevealed: (secret: RevealedSecret) => void;
}

/** Explicit, audited reveal step: explains the audit trail, picks a version and asks for typed confirmation. */
export function RevealSecretDialog({ secret, onOpenChange, onRevealed }: RevealSecretDialogProps) {
  const { t } = useTranslation('secrets');
  const open = secret !== undefined;
  const [version, setVersion] = useState(CURRENT);
  const [lastId, setLastId] = useState<string>();
  if (secret?.id !== lastId) {
    setLastId(secret?.id);
    setVersion(CURRENT);
  }
  const versions = useRecentSecretVersions(secret?.id ?? '', open);
  const options = versions.data?.versions ?? [];

  const reveal = async () => {
    if (!secret) return;
    const res = await secretAdminClient.revealSecret({
      id: secret.id,
      version: version === CURRENT ? 0 : Number(version),
    });
    onRevealed({ value: res.value, version: res.version });
  };

  return (
    <ConfirmDialog
      open={open}
      onOpenChange={onOpenChange}
      title={t('reveal.title', { path: secret?.path ?? '' })}
      description={t('reveal.description')}
      confirmLabel={t('reveal.confirm')}
      confirmText={REVEAL_CONFIRM_TEXT}
      onConfirm={reveal}
    >
      <div className="grid gap-3">
        <p className="flex items-start gap-2 rounded-md border border-amber-500/30 bg-amber-500/5 p-2 text-xs text-amber-700 dark:text-amber-400">
          <ShieldAlertIcon className="mt-0.5 size-4 shrink-0" aria-hidden />
          {t('reveal.audit')}
        </p>
        <FormField label={t('reveal.version')}>
          <Select value={version} onValueChange={setVersion} disabled={versions.isLoading}>
            <SelectTrigger className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={CURRENT}>
                {t('reveal.currentVersion', { version: secret?.currentVersion ?? 0 })}
              </SelectItem>
              {options
                .filter((v) => v.version !== secret?.currentVersion)
                .map((v) => (
                  <SelectItem key={v.version} value={String(v.version)}>
                    <span className="tabular">v{v.version}</span>
                    <span className="text-muted-foreground">{formatDateTime(v.createdAt, false)}</span>
                  </SelectItem>
                ))}
            </SelectContent>
          </Select>
        </FormField>
      </div>
    </ConfirmDialog>
  );
}
