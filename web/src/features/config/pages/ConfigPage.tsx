import { useNavigate, useSearch } from '@tanstack/react-router';
import { FileCode2Icon, PlusIcon, RefreshCwIcon } from 'lucide-react';
import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';

import { useAuth } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';
import { PermissionButton, RequirePermission } from '@/app/auth/PermissionGate';
import { PaginationControls, useCursorPagination } from '@/components/data-table';
import { EmptyState } from '@/components/EmptyState';
import { SearchInput } from '@/components/FilterBar';
import { PageHeader } from '@/components/PageHeader';
import { PageIntro } from '@/components/PageIntro';
import { Button } from '@/components/ui/button';
import { Card } from '@/components/ui/card';

import { ConfigItemPanel } from '../components/ConfigItemPanel';
import { ConfigTree } from '../components/ConfigTree';
import { CreateConfigDialog } from '../components/CreateConfigDialog';
import { buildConfigTree } from '../configModel';
import { useConfigItems, type ConfigSelection } from '../useConfigApi';

const TREE_PAGE_SIZES = [100, 200, 500] as const;

function parseSelection(search: Record<string, unknown>): ConfigSelection | undefined {
  const { group, key } = search;
  if (typeof group !== 'string' || typeof key !== 'string' || group === '' || key === '') return undefined;
  return { group, key };
}

function ConfigContent() {
  const { t } = useTranslation('config');
  const { tenantId, namespaceName } = useAuth();
  const search = useSearch({ from: '/_app/config' });
  const navigate = useNavigate({ from: '/config' });
  const selection = parseSelection(search);
  const [query, setQuery] = useState('');
  const [createOpen, setCreateOpen] = useState(false);
  const pager = useCursorPagination({ pageSize: 200, resetOn: [tenantId, namespaceName, query] });
  const items = useConfigItems(query, pager);
  const tree = useMemo(() => buildConfigTree(items.data?.items ?? []), [items.data]);

  const select = (next: ConfigSelection | undefined) => {
    if (next?.group === selection?.group && next?.key === selection?.key) return;
    void navigate({
      search: (prev) => ({ ...prev, group: next?.group, key: next?.key }),
      replace: next === undefined,
    });
  };

  if (!namespaceName) {
    return <EmptyState title={t('noNamespace')} />;
  }

  return (
    <>
      <PageHeader
        title={t('title')}
        description={t('description', { namespace: namespaceName })}
        actions={
          <>
            <Button
              variant="outline"
              size="icon-sm"
              aria-label={t('common:actions.refresh')}
              onClick={() => void items.refetch()}
            >
              <RefreshCwIcon className={items.isFetching ? 'animate-spin' : undefined} />
            </Button>
            <PermissionButton
              permission={PERMISSIONS.configWrite}
              size="sm"
              onClick={() => setCreateOpen(true)}
            >
              <PlusIcon />
              {t('create.button')}
            </PermissionButton>
          </>
        }
      />
      <PageIntro page="config" links={[{ to: '/secrets', labelKey: 'nav.secrets' }]} />
      <div className="grid items-start gap-4 lg:grid-cols-[18rem_minmax(0,1fr)]">
        <Card className="gap-0 overflow-hidden p-0 lg:sticky lg:top-4">
          <div className="border-b p-2">
            <SearchInput
              value={query}
              onChange={setQuery}
              placeholder={t('tree.search')}
              className="sm:w-full"
            />
          </div>
          <div className="max-h-[calc(100vh-18rem)] min-h-40 overflow-y-auto">
            <ConfigTree
              groups={tree}
              selected={selection}
              onSelect={select}
              isLoading={items.isLoading}
              error={items.error}
              onRetry={() => void items.refetch()}
              filtered={query !== ''}
            />
          </div>
          {(pager.pageIndex > 0 || Boolean(items.data?.nextPageToken)) && (
            <PaginationControls
              pager={pager}
              nextPageToken={items.data?.nextPageToken}
              total={items.data?.total}
              pageSizeOptions={TREE_PAGE_SIZES}
              rowCount={items.data?.items.length ?? 0}
            />
          )}
        </Card>
        <section className="min-w-0">
          {selection ? (
            <ConfigItemPanel
              key={`${tenantId ?? ''}/${namespaceName}/${selection.group}/${selection.key}`}
              selection={selection}
              onDeleted={() => select(undefined)}
              onClearSelection={() => select(undefined)}
            />
          ) : (
            <EmptyState
              icon={FileCode2Icon}
              title={t('selectItem')}
              description={t('selectItemDescription')}
            />
          )}
        </section>
      </div>
      <CreateConfigDialog
        open={createOpen}
        onOpenChange={setCreateOpen}
        defaultGroup={selection?.group}
        onCreated={select}
      />
    </>
  );
}

/** Config center: group/key tree, editor with drafts, publish, versions, diff and rollback. */
export default function ConfigPage() {
  return (
    <RequirePermission permission={PERMISSIONS.configRead}>
      <ConfigContent />
    </RequirePermission>
  );
}
