import { EyeIcon, PencilIcon, Trash2Icon } from 'lucide-react';
import { useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';

import { PERMISSIONS } from '@/app/auth/permissions';
import { PermissionButton } from '@/app/auth/PermissionGate';
import { IdText } from '@/components/CopyButton';
import { ErrorState } from '@/components/ErrorState';
import { TimeAgo } from '@/components/TimeAgo';
import { Badge } from '@/components/ui/badge';
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '@/components/ui/sheet';
import { Skeleton } from '@/components/ui/skeleton';
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs';
import { type SecretInfo } from '@/gen/spinneret/v1/secret_admin_pb';

import { useSecret } from '../useSecretsApi';
import { ExpiryCell } from './ExpiryCell';
import { SecretAccessLogsTab } from './SecretAccessLogsTab';
import { SecretVersionsTab } from './SecretVersionsTab';

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid grid-cols-[9rem_1fr] items-start gap-3 py-2 text-sm">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0 break-words">{children}</dd>
    </div>
  );
}

export interface SecretDetailSheetProps {
  /** Row data shown immediately; refreshed with GetSecret. */
  secret: SecretInfo | undefined;
  onOpenChange: (open: boolean) => void;
  onReveal: (secret: SecretInfo) => void;
  onEdit: (secret: SecretInfo) => void;
  onDelete: (secret: SecretInfo) => void;
}

function Overview({ secret }: { secret: SecretInfo }) {
  const { t } = useTranslation('secrets');
  return (
    <dl className="divide-y">
      <Row label={t('fields.id')}>
        <IdText value={secret.id} />
      </Row>
      <Row label={t('fields.maskedValue')}>
        <span className="font-mono text-xs">{secret.maskedValue || '—'}</span>
      </Row>
      <Row label={t('fields.version')}>
        <span className="tabular">v{secret.currentVersion}</span>
      </Row>
      <Row label={t('fields.description')}>{secret.description || '—'}</Row>
      <Row label={t('fields.tags')}>
        {secret.tags.length === 0 ? (
          '—'
        ) : (
          <span className="flex flex-wrap gap-1">
            {secret.tags.map((tag) => (
              <Badge key={tag} variant="outline">
                {tag}
              </Badge>
            ))}
          </span>
        )}
      </Row>
      <Row label={t('fields.expiresAt')}>
        <ExpiryCell value={secret.expiresAt} />
      </Row>
      <Row label={t('fields.lastAccessed')}>
        <TimeAgo value={secret.lastAccessedAt} fallback={t('common:time.never')} past />
      </Row>
      <Row label={t('fields.createdBy')}>
        <span className="font-mono text-xs">{secret.createdBy || '—'}</span>
      </Row>
      <Row label={t('fields.createdAt')}>
        <TimeAgo value={secret.createdAt} past />
      </Row>
      <Row label={t('fields.updatedAt')}>
        <TimeAgo value={secret.updatedAt} past />
      </Row>
    </dl>
  );
}

/** Side panel of a secret: metadata, versions and access logs, with reveal/edit/delete actions. */
export function SecretDetailSheet({
  secret,
  onOpenChange,
  onReveal,
  onEdit,
  onDelete,
}: SecretDetailSheetProps) {
  const { t } = useTranslation('secrets');
  const open = secret !== undefined;
  const [tab, setTab] = useState('overview');
  // Keep the last secret while the sheet animates closed; start on the overview for another secret.
  const [shown, setShown] = useState(secret);
  if (secret !== undefined && secret !== shown) {
    setShown(secret);
    if (secret.id !== shown?.id) setTab('overview');
  }
  const detail = useSecret(open ? secret.id : undefined);
  const current = open ? (detail.data ?? secret) : shown;

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="w-full sm:max-w-3xl">
        <SheetHeader className="border-b">
          <SheetTitle className="font-mono break-all">{current?.path}</SheetTitle>
          <SheetDescription>{t('detail.description')}</SheetDescription>
          {current && (
            <div className="flex flex-wrap gap-2 pt-2">
              <PermissionButton
                permission={PERMISSIONS.secretReveal}
                size="sm"
                onClick={() => onReveal(current)}
              >
                <EyeIcon />
                {t('actions.reveal')}
              </PermissionButton>
              <PermissionButton
                permission={PERMISSIONS.secretWrite}
                size="sm"
                variant="outline"
                onClick={() => onEdit(current)}
              >
                <PencilIcon />
                {t('actions.edit')}
              </PermissionButton>
              <PermissionButton
                permission={PERMISSIONS.secretWrite}
                size="sm"
                variant="outline"
                className="text-destructive hover:text-destructive"
                onClick={() => onDelete(current)}
              >
                <Trash2Icon />
                {t('common:actions.delete')}
              </PermissionButton>
            </div>
          )}
        </SheetHeader>
        <div className="px-4 pb-4">
          {detail.isError && !current ? (
            <ErrorState error={detail.error} onRetry={() => void detail.refetch()} compact />
          ) : !current ? (
            <Skeleton className="h-64 w-full" />
          ) : (
            <Tabs value={tab} onValueChange={setTab}>
              <TabsList>
                <TabsTrigger value="overview">{t('detail.overview')}</TabsTrigger>
                <TabsTrigger value="versions">{t('detail.versions')}</TabsTrigger>
                <TabsTrigger value="logs">{t('detail.accessLogs')}</TabsTrigger>
              </TabsList>
              <TabsContent value="overview">
                <Overview secret={current} />
              </TabsContent>
              <TabsContent value="versions">
                <SecretVersionsTab secret={current} />
              </TabsContent>
              <TabsContent value="logs">
                <SecretAccessLogsTab secret={current} />
              </TabsContent>
            </Tabs>
          )}
        </div>
      </SheetContent>
    </Sheet>
  );
}
