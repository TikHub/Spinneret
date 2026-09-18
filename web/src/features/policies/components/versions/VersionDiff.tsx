import { ArrowRightIcon } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { DiffView } from '@/components/editor/DiffView';
import { ErrorState } from '@/components/ErrorState';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { Skeleton } from '@/components/ui/skeleton';
import { type Policy } from '@/gen/spinneret/v1/policy_admin_pb';

import { defaultDiffSides, versionNumbers } from '../../selectors';
import { usePolicyDiff, type DiffSides } from '../../usePolicyQueries';

export interface VersionDiffProps {
  policy: Policy;
  sides: DiffSides | undefined;
  onSidesChange: (sides: DiffSides) => void;
}

/** Side-by-side diff between two versions, or a version and the draft (DiffPolicyVersions). */
export function VersionDiff({ policy, sides: requested, onSidesChange }: VersionDiffProps) {
  const { t } = useTranslation('policies');
  // "Draft" (to = 0) is only valid while a draft exists; after publishing fall back to the default sides.
  const sides = requested && requested.to === 0 && !policy.hasDraft ? defaultDiffSides(policy) : requested;
  const diff = usePolicyDiff(policy.id, sides);
  const numbers = versionNumbers(policy.currentVersion);
  const fromValue = sides ? String(sides.from) : '';
  const toValue = sides ? String(sides.to) : '';
  const update = (patch: Partial<DiffSides>) =>
    onSidesChange({ from: sides?.from ?? 0, to: sides?.to ?? policy.currentVersion, ...patch });

  return (
    <section className="grid gap-3 rounded-lg border bg-card p-4">
      <div className="flex flex-wrap items-center gap-2">
        <h3 className="mr-2 text-sm font-semibold">{t('versions.compare')}</h3>
        <Select value={fromValue} onValueChange={(v) => update({ from: Number(v) })}>
          <SelectTrigger size="sm" className="w-52" aria-label={t('versions.from')}>
            <SelectValue placeholder={t('versions.from')} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="0">
              {t('versions.currentPublished', { version: policy.currentVersion })}
            </SelectItem>
            {numbers.map((n) => (
              <SelectItem key={n} value={String(n)}>
                {t('versions.versionLabel', { version: n })}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <ArrowRightIcon className="size-4 text-muted-foreground" aria-hidden />
        <Select value={toValue} onValueChange={(v) => update({ to: Number(v) })}>
          <SelectTrigger size="sm" className="w-52" aria-label={t('versions.to')}>
            <SelectValue placeholder={t('versions.to')} />
          </SelectTrigger>
          <SelectContent>
            {policy.hasDraft && <SelectItem value="0">{t('versions.draft')}</SelectItem>}
            {numbers.map((n) => (
              <SelectItem key={n} value={String(n)}>
                {t('versions.versionLabel', { version: n })}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      {!sides ? (
        <p className="py-6 text-center text-sm text-muted-foreground">{t('versions.pickSides')}</p>
      ) : diff.isLoading ? (
        <Skeleton className="h-[420px] w-full" />
      ) : diff.isError ? (
        <ErrorState error={diff.error} onRetry={() => void diff.refetch()} compact />
      ) : diff.data ? (
        <>
          {diff.data.fromYaml === diff.data.toYaml && (
            <p className="text-sm text-muted-foreground">{t('versions.identical')}</p>
          )}
          <DiffView original={diff.data.fromYaml} modified={diff.data.toYaml} language="yaml" height={460} />
        </>
      ) : null}
    </section>
  );
}
