import { type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';

import { CopyButton } from '@/components/CopyButton';
import { JsonView } from '@/components/JsonView';
import { Badge } from '@/components/ui/badge';
import { type Credential } from '@/gen/spinneret/v1/lease_pb';

import { CREDENTIAL_SEGMENTS, credentialView, isSegmentUsed, type CredentialSegment } from '../credential';

function Segment({ name, children }: { name: CredentialSegment; children: ReactNode }) {
  const { t } = useTranslation('identity-types');
  return (
    <section className="grid gap-1.5">
      <h4 className="flex items-baseline gap-2 text-sm font-medium">
        <code className="font-mono">{name}</code>
        <span className="text-xs font-normal text-muted-foreground">{t(`preview.segments.${name}`)}</span>
      </h4>
      {children}
    </section>
  );
}

function KeyValueTable({ entries }: { entries: ReadonlyArray<[string, string]> }) {
  const { t } = useTranslation('identity-types');
  return (
    <div className="overflow-x-auto rounded-md border">
      <table className="w-full text-xs">
        <thead className="bg-muted/60 text-left text-muted-foreground">
          <tr>
            <th scope="col" className="px-2 py-1.5 font-medium">
              {t('preview.name')}
            </th>
            <th scope="col" className="px-2 py-1.5 font-medium">
              {t('preview.value')}
            </th>
          </tr>
        </thead>
        <tbody>
          {entries.map(([key, value]) => (
            <tr key={key} className="border-t align-top">
              <td className="px-2 py-1.5 font-mono whitespace-nowrap">{key}</td>
              <td className="px-2 py-1.5 font-mono break-all">{value}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/** Rendered delivery credential: the six segments a node merges into its request. */
export function CredentialPanel({ credential }: { credential: Credential | undefined }) {
  const { t } = useTranslation('identity-types');
  const view = credentialView(credential);
  const unused = CREDENTIAL_SEGMENTS.filter((segment) => !isSegmentUsed(view, segment));

  return (
    <div className="grid gap-3">
      {unused.length === CREDENTIAL_SEGMENTS.length && (
        <p className="text-sm text-muted-foreground">{t('preview.emptyCredential')}</p>
      )}
      {isSegmentUsed(view, 'cookies') && (
        <Segment name="cookies">
          <KeyValueTable entries={view.cookies} />
        </Segment>
      )}
      {isSegmentUsed(view, 'cookie_header') && (
        <Segment name="cookie_header">
          <div className="flex items-start gap-1 rounded-md border bg-muted/30 p-2">
            <code className="min-w-0 flex-1 font-mono text-xs break-all">{view.cookieHeader}</code>
            <CopyButton value={view.cookieHeader} />
          </div>
        </Segment>
      )}
      {isSegmentUsed(view, 'headers') && (
        <Segment name="headers">
          <KeyValueTable entries={view.headers} />
        </Segment>
      )}
      {isSegmentUsed(view, 'query') && (
        <Segment name="query">
          <KeyValueTable entries={view.query} />
        </Segment>
      )}
      {isSegmentUsed(view, 'json') && (
        <Segment name="json">
          <JsonView value={view.json} />
        </Segment>
      )}
      {isSegmentUsed(view, 'values') && (
        <Segment name="values">
          <JsonView value={view.values} />
        </Segment>
      )}
      {unused.length > 0 && unused.length < CREDENTIAL_SEGMENTS.length && (
        <p className="flex flex-wrap items-center gap-1 text-xs text-muted-foreground">
          {t('preview.unusedSegments')}
          {unused.map((segment) => (
            <Badge key={segment} variant="muted" className="font-mono font-normal">
              {segment}
            </Badge>
          ))}
        </p>
      )}
    </div>
  );
}
