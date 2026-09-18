import { Link } from '@tanstack/react-router';
import { FlaskConicalIcon, ListOrderedIcon, LoaderCircleIcon } from 'lucide-react';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';

import { IdText } from '@/components/CopyButton';
import { ErrorState } from '@/components/ErrorState';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { FormField } from '@/components/ui/form';
import { Input } from '@/components/ui/input';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { type Site, type TestURIResponse } from '@/gen/spinneret/v1/site_admin_pb';
import { cn } from '@/lib/utils';

import { useTestURI } from '../useSites';
import { type RuleEditorTarget } from './URIRulesSheet';

/** Maximum URI length (TestURIRequest.uri). */
const MAX_URI_LENGTH = 2048;

/** Badge colors per binding level, from most to least specific. */
const LEVEL_CLASS: Record<string, string> = {
  endpoint_group: 'border-indigo-500/30 bg-indigo-500/10 text-indigo-700 dark:text-indigo-400',
  client: 'border-sky-500/30 bg-sky-500/10 text-sky-700 dark:text-sky-400',
  site: 'border-emerald-500/30 bg-emerald-500/10 text-emerald-700 dark:text-emerald-400',
  namespace: 'border-amber-500/30 bg-amber-500/10 text-amber-700 dark:text-amber-400',
  builtin: 'border-zinc-500/30 bg-zinc-500/10 text-zinc-600 dark:text-zinc-400',
};

function TestResult({
  result,
  client,
  site,
  onOpenRules,
}: {
  result: TestURIResponse;
  client: string;
  site: Site;
  onOpenRules: (target: RuleEditorTarget) => void;
}) {
  const { t } = useTranslation('sites');
  return (
    <div className="grid gap-3" aria-live="polite">
      <dl className="grid gap-3 sm:grid-cols-2">
        <div className="grid gap-1">
          <dt className="text-xs text-muted-foreground">{t('tester.group')}</dt>
          <dd className="flex flex-wrap items-center gap-2">
            <span className="font-mono text-sm font-medium">{result.endpointGroup}</span>
            {result.isDefault && <Badge variant="outline">{t('tester.defaultGroup')}</Badge>}
            <IdText value={result.endpointGroupId} truncate={14} className="text-muted-foreground" />
          </dd>
        </div>
        <div className="grid gap-1">
          <dt className="text-xs text-muted-foreground">{t('tester.rule')}</dt>
          <dd className="flex flex-wrap items-center gap-2 text-sm">
            {result.isDefault ? (
              <span className="text-muted-foreground">{t('tester.noRuleMatched')}</span>
            ) : (
              <>
                <Badge variant="secondary">
                  {t(`rules.kinds.${result.kind}`, { defaultValue: result.kind })}
                </Badge>
                <IdText value={result.ruleId} truncate={14} />
              </>
            )}
            {result.endpointGroupId && (
              <Button
                variant="link"
                size="sm"
                className="h-auto p-0"
                onClick={() =>
                  onOpenRules({
                    id: result.endpointGroupId,
                    name: result.endpointGroup,
                    client,
                    site: site.name,
                    siteId: site.id,
                  })
                }
              >
                <ListOrderedIcon />
                {t('tester.openRules')}
              </Button>
            )}
          </dd>
        </div>
      </dl>
      <div className="overflow-x-auto rounded-md border">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t('tester.policyKind')}</TableHead>
              <TableHead>{t('tester.policy')}</TableHead>
              <TableHead className="text-right">{t('tester.version')}</TableHead>
              <TableHead>{t('tester.level')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {result.policies.map((policy) => (
              <TableRow key={policy.kind}>
                <TableCell>{t(`tester.kinds.${policy.kind}`, { defaultValue: policy.kind })}</TableCell>
                <TableCell>
                  {policy.policyId ? (
                    <Link
                      to="/policies"
                      search={{ id: policy.policyId }}
                      className="font-medium text-primary underline-offset-4 hover:underline"
                    >
                      {policy.name || policy.policyId}
                    </Link>
                  ) : (
                    <span className="text-muted-foreground">{policy.name || t('tester.builtin')}</span>
                  )}
                </TableCell>
                <TableCell className="tabular text-right">
                  {policy.version > 0 ? `v${policy.version}` : '—'}
                </TableCell>
                <TableCell>
                  <span
                    className={cn(
                      'inline-flex rounded-full border px-2 py-0.5 text-xs font-medium',
                      LEVEL_CLASS[policy.level] ?? LEVEL_CLASS.builtin,
                    )}
                  >
                    {t(`tester.levels.${policy.level}`, { defaultValue: policy.level })}
                  </span>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
    </div>
  );
}

export interface URITesterPanelProps {
  site: Site;
  /** Client preselected (the active client tab). */
  defaultClient: string;
  onOpenRules: (target: RuleEditorTarget) => void;
}

/** Resolves a URI to its endpoint group, matching rule and effective policies (TestURI). */
export function URITesterPanel({ site, defaultClient, onOpenRules }: URITesterPanelProps) {
  const { t } = useTranslation('sites');
  const [client, setClient] = useState(defaultClient);
  const [uri, setUri] = useState('');
  const mutation = useTestURI();
  const activeClient = site.clients.includes(client) ? client : (site.clients[0] ?? '');

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (uri.trim() === '' || activeClient === '') return;
    mutation.mutate({ site: site.name, client: activeClient, uri: uri.trim() });
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <FlaskConicalIcon className="size-4 text-muted-foreground" aria-hidden />
          {t('tester.title')}
        </CardTitle>
        <CardDescription>{t('tester.description')}</CardDescription>
      </CardHeader>
      <CardContent className="grid gap-4">
        <form className="flex flex-wrap items-end gap-2" onSubmit={submit}>
          <FormField label={t('tester.client')} className="w-40">
            <Select value={activeClient} onValueChange={setClient}>
              <SelectTrigger className="w-full font-mono">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {site.clients.map((c) => (
                  <SelectItem key={c} value={c} className="font-mono">
                    {c}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </FormField>
          <FormField label={t('tester.uri')} className="min-w-64 flex-1">
            <Input
              value={uri}
              onChange={(e) => setUri(e.target.value)}
              placeholder={t('tester.uriPlaceholder')}
              className="font-mono"
              spellCheck={false}
              autoComplete="off"
              maxLength={MAX_URI_LENGTH}
            />
          </FormField>
          <Button type="submit" disabled={mutation.isPending || uri.trim() === '' || activeClient === ''}>
            {mutation.isPending && <LoaderCircleIcon className="animate-spin" />}
            {t('tester.run')}
          </Button>
        </form>
        {mutation.isError && <ErrorState error={mutation.error} compact className="py-4" />}
        {mutation.data && !mutation.isError && (
          <TestResult
            result={mutation.data}
            client={mutation.variables?.client ?? activeClient}
            site={site}
            onOpenRules={onOpenRules}
          />
        )}
      </CardContent>
    </Card>
  );
}
