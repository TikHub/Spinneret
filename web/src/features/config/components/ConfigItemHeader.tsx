import { LockIcon, PencilLineIcon } from 'lucide-react';
import { type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';

import { CopyButton, IdText } from '@/components/CopyButton';
import { TimeAgo } from '@/components/TimeAgo';
import { Badge } from '@/components/ui/badge';
import { type ConfigItemInfo } from '@/gen/spinneret/v1/config_admin_pb';

import { itemLabel } from '../configModel';

function Meta({ label, children }: { label: string; children: ReactNode }) {
  return (
    <span className="inline-flex items-center gap-1 text-xs text-muted-foreground">
      <span>{label}</span>
      <span className="text-foreground">{children}</span>
    </span>
  );
}

export interface ConfigItemHeaderProps {
  item: ConfigItemInfo;
  readOnly: boolean;
  dirty: boolean;
  actions: ReactNode;
}

/** Title, state badges, metadata and actions of the selected config item. */
export function ConfigItemHeader({ item, readOnly, dirty, actions }: ConfigItemHeaderProps) {
  const { t } = useTranslation('config');
  const label = itemLabel(item);
  return (
    <div className="grid gap-2 border-b pb-3">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="grid min-w-0 gap-1">
          <div className="flex min-w-0 flex-wrap items-center gap-1.5">
            <h2 className="truncate font-mono text-base font-semibold" title={label}>
              {label}
            </h2>
            <CopyButton value={label} label={t('item.copyLocator')} />
            <Badge variant="outline">{t(`formats.${item.format}`, { defaultValue: item.format })}</Badge>
            {item.currentVersion > 0 ? (
              <Badge variant="secondary" className="tabular">
                {t('item.version', { version: item.currentVersion })}
              </Badge>
            ) : (
              <Badge variant="muted">{t('item.neverPublished')}</Badge>
            )}
            {item.hasDraft && (
              <Badge
                className="border-amber-500/30 bg-amber-500/10 text-amber-700 dark:text-amber-400"
                variant="outline"
              >
                <PencilLineIcon />
                {t('item.draft')}
              </Badge>
            )}
            {dirty && (
              <Badge variant="outline" className="border-primary/40 text-primary">
                {t('item.unsaved')}
              </Badge>
            )}
            {readOnly && (
              <Badge variant="outline" className="border-amber-500/30 text-amber-700 dark:text-amber-400">
                <LockIcon />
                {t('item.readOnly')}
              </Badge>
            )}
          </div>
          {item.description && <p className="text-sm text-muted-foreground">{item.description}</p>}
        </div>
        <div className="flex flex-wrap items-center gap-2">{actions}</div>
      </div>
      <div className="flex flex-wrap gap-x-4 gap-y-1">
        {item.id && (
          <Meta label={t('item.id')}>
            <IdText value={item.id} />
          </Meta>
        )}
        {item.currentVersion > 0 && (
          <Meta label={t('item.published')}>
            <TimeAgo value={item.publishedAt} past />
            {item.publishedBy && (
              <span className="ml-1 font-mono text-muted-foreground">{item.publishedBy}</span>
            )}
          </Meta>
        )}
        {item.hasDraft && (
          <Meta label={t('item.draftSaved')}>
            <TimeAgo value={item.draftUpdatedAt} past />
            {item.draftUpdatedBy && (
              <span className="ml-1 font-mono text-muted-foreground">{item.draftUpdatedBy}</span>
            )}
          </Meta>
        )}
      </div>
    </div>
  );
}
