import { KeyRoundIcon } from 'lucide-react';
import { Trans, useTranslation } from 'react-i18next';

import { Badge } from '@/components/ui/badge';
import { cn } from '@/lib/utils';

import { formatSecretRef, type SecretRef } from '../secretRefs';

export interface SecretRefHelpProps {
  /** One-line hint (create dialog). */
  compact?: boolean;
  /** References found in the current content. */
  refs?: readonly SecretRef[];
  className?: string;
}

const CODE = <code className="rounded bg-muted px-1 font-mono text-[11px] text-foreground" />;

/** Explains ${secret:path} references: kept verbatim in the console, resolved for nodes. */
export function SecretRefHelp({ compact = false, refs, className }: SecretRefHelpProps) {
  const { t } = useTranslation('config');
  if (compact) {
    return (
      <p className={cn('flex items-start gap-1.5 text-xs text-muted-foreground', className)}>
        <KeyRoundIcon className="mt-0.5 size-3.5 shrink-0" aria-hidden />
        <span>
          <Trans t={t} i18nKey="secretRefs.short" components={{ code: CODE }} />
        </span>
      </p>
    );
  }
  return (
    <div className={cn('grid gap-2 rounded-md border bg-muted/30 p-3 text-xs', className)}>
      <p className="flex items-center gap-1.5 font-medium text-foreground">
        <KeyRoundIcon className="size-3.5" aria-hidden />
        {t('secretRefs.title')}
      </p>
      <p className="text-muted-foreground">
        <Trans t={t} i18nKey="secretRefs.syntax" components={{ code: CODE }} />
      </p>
      <p className="text-muted-foreground">{t('secretRefs.resolution')}</p>
      {refs && (
        <div className="flex flex-wrap items-center gap-1">
          <span className="text-muted-foreground">{t('secretRefs.found', { count: refs.length })}</span>
          {refs.map((ref) => (
            <Badge key={formatSecretRef(ref)} variant="outline" className="font-mono">
              {formatSecretRef(ref)}
            </Badge>
          ))}
        </div>
      )}
    </div>
  );
}
