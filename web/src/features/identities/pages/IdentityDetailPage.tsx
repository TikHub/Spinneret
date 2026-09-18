import { useQuery } from '@tanstack/react-query';
import { Link, useParams } from '@tanstack/react-router';
import { ArrowLeftIcon, FingerprintIcon, RefreshCwIcon, SearchXIcon } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';

import { useAuth, useScopedQueryKey } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';
import { RequirePermission } from '@/app/auth/PermissionGate';
import { usePageTitle } from '@/app/pageTitle';
import { LIVE_REFETCH_MS } from '@/app/queryClient';
import { CopyButton } from '@/components/CopyButton';
import { EmptyState } from '@/components/EmptyState';
import { ErrorState } from '@/components/ErrorState';
import { PageHeader } from '@/components/PageHeader';
import { PageIntro } from '@/components/PageIntro';
import { StateBadge } from '@/components/StateBadge';
import { Button } from '@/components/ui/button';
import { Skeleton } from '@/components/ui/skeleton';
import { type BulkResult } from '@/gen/spinneret/v1/common_pb';
import { identityClient } from '@/lib/clients';
import { isNotFound } from '@/lib/errors';
import { formatDateTime, toDate } from '@/lib/time';

import { AttributesCard } from '../components/detail/AttributesCard';
import { EndpointHotStateTable } from '../components/detail/EndpointHotStateTable';
import { MetadataEditDialog } from '../components/detail/MetadataEditDialog';
import { PayloadCard } from '../components/detail/PayloadCard';
import { PayloadEditDialog } from '../components/detail/PayloadEditDialog';
import { RiskEventsCard } from '../components/detail/RiskEventsCard';
import { SiteHotStateCard } from '../components/detail/SiteHotStateCard';
import { StateEventTimeline } from '../components/detail/StateEventTimeline';
import { OperationDialog } from '../components/OperationDialog';
import { OperationsMenu } from '../components/OperationsMenu';
import { notifyBulkResult, useInvalidateIdentityData } from '../notify';
import { availableOperations, type IdentityOperation } from '../operations';
import { useRevealedPayload } from '../reveal';

type DetailDialog =
  | { kind: 'operate'; operation: IdentityOperation; seq: number }
  | { kind: 'metadata'; seq: number }
  | { kind: 'payload'; seq: number };

function DetailSkeleton() {
  return (
    <div className="grid gap-4">
      <Skeleton className="h-10 w-96" />
      <div className="grid gap-4 lg:grid-cols-2">
        <Skeleton className="h-80 w-full" />
        <Skeleton className="h-80 w-full" />
      </div>
      <Skeleton className="h-48 w-full" />
    </div>
  );
}

function IdentityDetail({ id }: { id: string }) {
  const { t } = useTranslation('identities');
  const { namespaceName } = useAuth();
  const key = useScopedQueryKey();
  const invalidate = useInvalidateIdentityData();
  const reveal = useRevealedPayload(id);
  const [dialog, setDialog] = useState<DetailDialog | null>(null);
  const [innerDialogOpen, setInnerDialogOpen] = useState(false);
  usePageTitle(id);

  const query = useQuery({
    queryKey: key('identities', 'detail', id),
    queryFn: ({ signal }) => identityClient.getIdentity({ id }, { signal }),
    enabled: Boolean(namespaceName),
    refetchInterval: dialog || innerDialogOpen ? false : LIVE_REFETCH_MS,
  });

  if (!namespaceName) return <EmptyState icon={FingerprintIcon} title={t('noNamespace')} />;
  if (query.isLoading) return <DetailSkeleton />;
  if (query.isError && !query.data) {
    if (isNotFound(query.error)) {
      return (
        <EmptyState
          icon={SearchXIcon}
          title={t('detail.notFound')}
          description={t('detail.notFoundDescription', { id })}
          action={
            <Button asChild variant="outline" size="sm">
              <Link to="/identities">
                <ArrowLeftIcon />
                {t('detail.backToList')}
              </Link>
            </Button>
          }
        />
      );
    }
    return <ErrorState error={query.error} onRetry={() => void query.refetch()} />;
  }
  const data = query.data;
  const identity = data?.identity;
  if (!data || !identity) return <EmptyState title={t('detail.notFound')} />;

  const nextSeq = (dialog?.seq ?? 0) + 1;
  const close = () => setDialog(null);
  const until =
    identity.state === 'banned'
      ? identity.banUntil
      : identity.state === 'quarantined'
        ? identity.quarantineUntil
        : undefined;

  const onApplied = (operation: IdentityOperation, result: BulkResult | undefined) => {
    notifyBulkResult(t, t(`operations.${operation}`), result);
    invalidate();
  };

  return (
    <>
      <PageHeader
        title={
          <span className="inline-flex items-center gap-2">
            <span className="font-mono">{identity.id}</span>
            <CopyButton value={identity.id} />
            <StateBadge kind="identity" state={identity.state} />
          </span>
        }
        description={
          <>
            {identity.site} · {identity.client} · <span className="font-mono">{identity.type}</span>
            {identity.stateReason && <> · {t('state.reason', { reason: identity.stateReason })}</>}
            {toDate(until) ? (
              <> · {t('state.until', { time: formatDateTime(until) })}</>
            ) : (
              identity.state === 'banned' && <> · {t('state.permanent')}</>
            )}
          </>
        }
        actions={
          <>
            <Button asChild variant="ghost" size="sm">
              <Link to="/identities">
                <ArrowLeftIcon />
                {t('detail.backToList')}
              </Link>
            </Button>
            <Button
              variant="outline"
              size="icon-sm"
              onClick={() => void query.refetch()}
              aria-label={t('common:actions.refresh')}
            >
              <RefreshCwIcon className={query.isFetching ? 'animate-spin' : undefined} />
            </Button>
            <OperationsMenu
              operations={availableOperations(identity.state)}
              onSelect={(operation) => setDialog({ kind: 'operate', operation, seq: nextSeq })}
              label={t('detail.operations')}
              site={identity.site}
              variant="default"
            />
          </>
        }
      />
      <PageIntro
        page="identity-detail"
        links={[
          { to: '/identities', labelKey: 'nav.identities' },
          { to: '/risk-events', labelKey: 'nav.riskEvents' },
        ]}
      />
      <div className="grid gap-4">
        <div className="grid gap-4 xl:grid-cols-2">
          <AttributesCard identity={identity} onEdit={() => setDialog({ kind: 'metadata', seq: nextSeq })} />
          <SiteHotStateCard identity={identity} hot={data.hotState} />
        </div>
        <EndpointHotStateTable groups={data.hotState?.groups ?? []} />
        <div className="grid gap-4 xl:grid-cols-2">
          <PayloadCard
            identity={identity}
            maskedPayload={data.payload}
            reveal={reveal}
            onEdit={() => setDialog({ kind: 'payload', seq: nextSeq })}
            onDialogChange={setInnerDialogOpen}
          />
          <StateEventTimeline identityId={identity.id} />
        </div>
        <RiskEventsCard identityId={identity.id} site={identity.site} />
      </div>

      {dialog?.kind === 'operate' && (
        <OperationDialog
          key={`operate-${dialog.seq}`}
          open
          onOpenChange={(next) => !next && close()}
          operation={dialog.operation}
          ids={[identity.id]}
          site={identity.site}
          onApplied={(result) => onApplied(dialog.operation, result)}
        />
      )}
      {dialog?.kind === 'metadata' && (
        <MetadataEditDialog
          key={`metadata-${dialog.seq}`}
          identity={identity}
          open
          onOpenChange={(next) => !next && close()}
        />
      )}
      {dialog?.kind === 'payload' && (
        <PayloadEditDialog
          key={`payload-${dialog.seq}`}
          identity={identity}
          initialPayload={reveal.payload ?? data.payload}
          revealed={reveal.payload !== undefined || data.revealed}
          open
          onOpenChange={(next) => !next && close()}
          onSaved={reveal.hide}
        />
      )}
    </>
  );
}

/** Identity detail: attributes, live hot state, masked payload with reveal, timeline and risk events. */
export default function IdentityDetailPage() {
  const { id } = useParams({ from: '/_app/identities/$id' });
  return (
    <RequirePermission permission={PERMISSIONS.identityRead}>
      <IdentityDetail id={id} />
    </RequirePermission>
  );
}
