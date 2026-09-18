import { LockIcon } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { Badge } from '@/components/ui/badge';
import { SimpleTooltip } from '@/components/ui/tooltip';
import { type IdentityField } from '@/gen/spinneret/v1/identity_admin_pb';

const FIELDS_SHOWN = 3;

function FieldLine({ field }: { field: IdentityField }) {
  const { t } = useTranslation('identity-types');
  return (
    <span className="flex items-center gap-1">
      <span className="font-mono">{field.name}</span>
      <span className="opacity-70">{field.type}</span>
      {field.required && <span>{t('fields.requiredShort')}</span>}
      {field.sensitive && <LockIcon className="size-3" aria-label={t('fields.sensitive')} />}
    </span>
  );
}

/** Compact list of payload fields (name, sensitive lock) with all fields in a tooltip. */
export function FieldSummary({ fields }: { fields: readonly IdentityField[] }) {
  const { t } = useTranslation('identity-types');
  if (fields.length === 0) return <span className="text-muted-foreground">—</span>;
  const shown = fields.slice(0, FIELDS_SHOWN);
  const rest = fields.length - shown.length;
  return (
    <SimpleTooltip
      content={
        <span className="grid gap-0.5">
          {fields.map((field) => (
            <FieldLine key={field.name} field={field} />
          ))}
        </span>
      }
    >
      <span className="inline-flex max-w-72 items-center gap-1">
        <Badge variant="outline" className="tabular font-normal">
          {t('fields.count', { count: fields.length })}
        </Badge>
        {shown.map((field) => (
          <Badge key={field.name} variant="secondary" className="max-w-28 truncate font-mono font-normal">
            {field.sensitive && <LockIcon aria-label={t('fields.sensitive')} />}
            {field.name}
          </Badge>
        ))}
        {rest > 0 && <span className="text-xs text-muted-foreground">+{rest}</span>}
      </span>
    </SimpleTooltip>
  );
}
