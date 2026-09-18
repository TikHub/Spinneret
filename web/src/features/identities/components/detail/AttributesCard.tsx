import { Link } from '@tanstack/react-router';
import { PencilIcon } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { PERMISSIONS } from '@/app/auth/permissions';
import { PermissionButton } from '@/app/auth/PermissionGate';
import { IdText } from '@/components/CopyButton';
import { TimeAgo } from '@/components/TimeAgo';
import { Badge } from '@/components/ui/badge';
import { Card, CardAction, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { type Identity } from '@/gen/spinneret/v1/identity_admin_pb';

import { DetailList, DetailRow } from './DetailList';

export interface AttributesCardProps {
  identity: Identity;
  onEdit: () => void;
}

/** Identity attributes (site, type, account, region, tags, labels, versions and times). */
export function AttributesCard({ identity, onEdit }: AttributesCardProps) {
  const { t } = useTranslation('identities');
  const labels = Object.entries(identity.labels).sort(([a], [b]) => a.localeCompare(b));
  const dash = <span className="text-muted-foreground">—</span>;

  return (
    <Card>
      <CardHeader className="flex-row items-center">
        <CardTitle>{t('detail.attributes')}</CardTitle>
        <CardAction>
          <PermissionButton
            permission={PERMISSIONS.identityWrite}
            site={identity.site}
            variant="outline"
            size="sm"
            onClick={onEdit}
          >
            <PencilIcon />
            {t('detail.editMetadata')}
          </PermissionButton>
        </CardAction>
      </CardHeader>
      <CardContent>
        <DetailList>
          <DetailRow label={t('fields.site')}>
            {identity.site} <span className="text-muted-foreground">· {identity.client}</span>
          </DetailRow>
          <DetailRow label={t('fields.type')}>
            <Link
              to="/identity-types"
              search={{ site: identity.site, search: identity.type }}
              className="font-mono text-xs wrap-anywhere hover:underline"
            >
              {identity.type}
            </Link>
            {identity.typeId && <IdText value={identity.typeId} className="ml-2 text-muted-foreground" />}
          </DetailRow>
          <DetailRow label={t('fields.accountRef')}>
            {identity.accountRef ? (
              <span className="inline-flex max-w-full flex-wrap items-center gap-2">
                <Link
                  to="/accounts"
                  search={{ site: identity.site, search: identity.accountRef }}
                  className="min-w-0 font-mono text-xs wrap-anywhere hover:underline"
                >
                  {identity.accountRef}
                </Link>
                <IdText value={identity.accountId} className="text-muted-foreground" />
              </span>
            ) : (
              dash
            )}
          </DetailRow>
          <DetailRow label={t('fields.region')}>{identity.region || dash}</DetailRow>
          <DetailRow label={t('fields.tags')}>
            {identity.tags.length === 0 ? (
              dash
            ) : (
              <span className="flex flex-wrap gap-1">
                {identity.tags.map((tag) => (
                  <Badge key={tag} variant="secondary" className="font-normal">
                    {tag}
                  </Badge>
                ))}
              </span>
            )}
          </DetailRow>
          <DetailRow label={t('fields.labels')}>
            {labels.length === 0 ? (
              dash
            ) : (
              <span className="flex flex-wrap gap-1">
                {labels.map(([k, v]) => (
                  <Badge key={k} variant="outline" className="font-mono font-normal">
                    {k}={v}
                  </Badge>
                ))}
              </span>
            )}
          </DetailRow>
          <DetailRow label={t('columns.payloadVersion')}>
            <span className="tabular">v{identity.payloadVersion}</span>
          </DetailRow>
          <DetailRow label={t('detail.createdAt')}>
            <TimeAgo value={identity.createdAt} past />
          </DetailRow>
          <DetailRow label={t('detail.activatedAt')}>
            <TimeAgo value={identity.activatedAt} fallback={t('common:time.never')} past />
          </DetailRow>
          <DetailRow label={t('columns.lastUsed')}>
            <TimeAgo value={identity.lastUsedAt} fallback={t('common:time.never')} past />
          </DetailRow>
          <DetailRow label={t('detail.updatedAt')}>
            <TimeAgo value={identity.updatedAt} past />
          </DetailRow>
        </DetailList>
      </CardContent>
    </Card>
  );
}
