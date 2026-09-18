import { ChevronDownIcon } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';

import { CodeEditor } from '@/components/editor/CodeEditor';
import { ErrorState } from '@/components/ErrorState';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import { type ResolvedPolicy } from '@/gen/spinneret/v1/policy_admin_pb';
import { cn } from '@/lib/utils';

import { useResolvedPolicies, type ScopeSelection } from '../../usePolicyQueries';
import { KindBadge, LevelBadge } from '../labels';
import { EMPTY_SCOPE } from '../../selectors';
import { ScopeSelects } from './ScopeSelects';

export interface ResolvePanelProps {
  onOpenPolicy: (policyId: string) => void;
}

/** Effective policy of every kind for a site, client and endpoint group (ResolvePolicies). */
export function ResolvePanel({ onOpenPolicy }: ResolvePanelProps) {
  const { t } = useTranslation('policies');
  const [scope, setScope] = useState<ScopeSelection>(EMPTY_SCOPE);
  const resolved = useResolvedPolicies(scope);

  return (
    <div className="grid gap-4">
      <section className="grid gap-2 rounded-lg border bg-card p-4">
        <div className="space-y-0.5">
          <h2 className="text-sm font-semibold">{t('resolve.title')}</h2>
          <p className="text-xs text-muted-foreground">{t('resolve.description')}</p>
        </div>
        <ScopeSelects value={scope} onChange={setScope} />
      </section>
      {resolved.isLoading ? (
        <div className="grid gap-3 lg:grid-cols-2">
          {Array.from({ length: 4 }, (_, i) => (
            <Skeleton key={i} className="h-28 w-full rounded-lg" />
          ))}
        </div>
      ) : resolved.isError ? (
        <ErrorState error={resolved.error} onRetry={() => void resolved.refetch()} />
      ) : (
        <div className={cn('grid gap-3 lg:grid-cols-2', resolved.isFetching && 'opacity-70')}>
          {resolved.data?.policies.map((p) => (
            <ResolvedCard key={p.kind} policy={p} onOpenPolicy={onOpenPolicy} />
          ))}
        </div>
      )}
    </div>
  );
}

function ResolvedCard({
  policy,
  onOpenPolicy,
}: {
  policy: ResolvedPolicy;
  onOpenPolicy: (id: string) => void;
}) {
  const { t } = useTranslation('policies');
  const [open, setOpen] = useState(false);
  const builtin = policy.policyId === '';
  return (
    <Card>
      <CardHeader className="flex-row flex-wrap items-center gap-2 pb-3">
        <KindBadge kind={policy.kind} />
        {builtin ? (
          <span className="text-sm font-medium">{t('resolve.builtin')}</span>
        ) : (
          <Button
            variant="link"
            className="h-auto p-0 font-mono text-sm"
            onClick={() => onOpenPolicy(policy.policyId)}
          >
            {policy.name}
          </Button>
        )}
        {policy.version > 0 && (
          <Badge variant="secondary" className="font-mono">
            v{policy.version}
          </Badge>
        )}
        <span className="ml-auto flex items-center gap-1.5 text-xs text-muted-foreground">
          {t('resolve.level')}
          <LevelBadge level={policy.level} />
        </span>
      </CardHeader>
      <CardContent className="grid gap-2">
        <Button
          variant="ghost"
          size="sm"
          className="w-fit"
          aria-expanded={open}
          onClick={() => setOpen((v) => !v)}
        >
          <ChevronDownIcon className={cn('transition-transform', open && 'rotate-180')} />
          {open ? t('resolve.hideYaml') : t('resolve.showYaml')}
        </Button>
        {open && (
          <CodeEditor
            value={policy.yaml}
            readOnly
            height={300}
            path={`resolved-${policy.kind}.yaml`}
            aria-label={t('resolve.yamlLabel', { kind: policy.kind })}
          />
        )}
      </CardContent>
    </Card>
  );
}
